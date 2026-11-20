// dhtsearch-lt-sidecar — the libtorrent metadata-fetch foundation for
// DHTSearch, as a separate process.
//
// The Go server is a pure-Go static binary and never links
// libtorrent. This program hosts one libtorrent-rasterbar 2.x session
// configured for metadata-only crawling and serves fetch jobs from
// the Go supervisor over stdin/stdout using the framed-JSON protocol
// in sidecar/PROTOCOL.md (4-byte big-endian length prefix + JSON).
//
// Metadata-only, by construction:
//   * every torrent is added with torrent_flags::upload_mode, which
//     (per libtorrent's own docs) means it never makes piece
//     requests; ut_metadata (BEP 9) traffic is unaffected. The
//     auto_managed flag is cleared, because an auto-managed torrent
//     is periodically taken out of upload mode by the session.
//   * the torrent is removed (delete_files) the moment its metadata
//     arrives, and save_path points at a scratch directory.
//   * peer supply has four sources: the magnet's tracker list and
//     PEX (as before), the session's own DHT (get_peers for every
//     added torrent), and the per-fetch peer hints the Go side
//     forwards from its peer-hint cache (metadata.Item.Peers — the
//     same warm start the anacrolix fetcher consumes). The DHT is a
//     peer supply only: upload_mode still means this process never
//     serves payload data. LSD, UPnP and NAT-PMP stay off — a
//     metadata fetcher gains nothing from LAN discovery or inbound
//     port mapping, and both would only add inbound surface.
//   * the session listens on OS-assigned ports (port 0), so the
//     DHT's UDP socket can never collide with the crawler's DHT
//     ports (DHT_PORT + identity index) or the API/web ports on a
//     shared host.
//
// The session is driven from a single thread (the main loop below);
// the only other thread reads stdin frames into a queue. All session
// calls and all stdout writes happen on the main loop.

#include <libtorrent/address.hpp>
#include <libtorrent/alert_types.hpp>
#include <libtorrent/error_code.hpp>
#include <libtorrent/magnet_uri.hpp>
#include <libtorrent/session.hpp>
#include <libtorrent/session_params.hpp>
#include <libtorrent/session_stats.hpp>
#include <libtorrent/settings_pack.hpp>
#include <libtorrent/socket.hpp>
#include <libtorrent/torrent_flags.hpp>
#include <libtorrent/torrent_info.hpp>
#include <libtorrent/version.hpp>

#include <nlohmann/json.hpp>

#include <atomic>
#include <cerrno>
#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <deque>
#include <map>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

#include <unistd.h>

namespace lt = libtorrent;
using json = nlohmann::json;
using clock_type = std::chrono::steady_clock;

static const char* kSidecarVersion = "0.1.0";

// ---------------------------------------------------------------------------
// Frame IO (stdout writes happen only on the main loop thread)

static bool write_all(int fd, const char* data, size_t len) {
    while (len > 0) {
        ssize_t n = ::write(fd, data, len);
        if (n < 0) {
            if (errno == EINTR) continue;
            return false;
        }
        data += n;
        len -= static_cast<size_t>(n);
    }
    return true;
}

static bool read_all(int fd, char* data, size_t len) {
    while (len > 0) {
        ssize_t n = ::read(fd, data, len);
        if (n < 0) {
            if (errno == EINTR) continue;
            return false;
        }
        if (n == 0) return false; // EOF
        data += n;
        len -= static_cast<size_t>(n);
    }
    return true;
}

static void send_frame(const json& msg) {
    std::string body = msg.dump();
    std::uint32_t n = static_cast<std::uint32_t>(body.size());
    char hdr[4] = {static_cast<char>(n >> 24), static_cast<char>(n >> 16),
                   static_cast<char>(n >> 8), static_cast<char>(n)};
    if (!write_all(STDOUT_FILENO, hdr, 4) ||
        !write_all(STDOUT_FILENO, body.data(), body.size())) {
        _exit(3); // parent is gone or the pipe broke; nothing to salvage
    }
}

static void log_line(const std::string& s) {
    std::fprintf(stderr, "%s\n", s.c_str());
    std::fflush(stderr);
}

// ---------------------------------------------------------------------------
// Inbound message queue (reader thread -> main loop)

struct Inbound {
    std::mutex mu;
    std::condition_variable cv;
    std::deque<json> queue;
    std::atomic<bool> eof{false};
};

static Inbound g_in;

static void reader_thread() {
    for (;;) {
        char hdr[4];
        if (!read_all(STDIN_FILENO, hdr, 4)) break;
        std::uint32_t n = (static_cast<std::uint32_t>(static_cast<unsigned char>(hdr[0])) << 24) |
                          (static_cast<std::uint32_t>(static_cast<unsigned char>(hdr[1])) << 16) |
                          (static_cast<std::uint32_t>(static_cast<unsigned char>(hdr[2])) << 8) |
                          static_cast<std::uint32_t>(static_cast<unsigned char>(hdr[3]));
        if (n == 0 || n > (4u << 20)) break; // protocol violation; framing is lost
        std::string body(n, '\0');
        if (!read_all(STDIN_FILENO, body.data(), n)) break;
        try {
            json msg = json::parse(body);
            {
                std::lock_guard<std::mutex> lk(g_in.mu);
                g_in.queue.push_back(std::move(msg));
            }
            g_in.cv.notify_one();
        } catch (const std::exception& e) {
            log_line(std::string("lt-sidecar: bad frame: ") + e.what());
        }
    }
    g_in.eof = true;
    g_in.cv.notify_one();
}

// ---------------------------------------------------------------------------
// Jobs

struct Job {
    std::uint64_t id = 0;
    lt::torrent_handle handle;
    clock_type::time_point deadline;
    int max_files = 50;
    int last_peers = 0;
    bool peer_reported = false;
};

// Record parity with the anacrolix fetcher (metadata.Record):
// libtorrent's file_storage paths for multi-file torrents carry the
// torrent root name as the first component ("Name/sub/file"), while
// anacrolix's DisplayPath is the path *within* the torrent
// ("sub/file"); for a single-file torrent DisplayPath is the torrent
// name itself. Normalise here so a Record looks the same whichever
// foundation produced it.
static std::string record_path(const lt::torrent_info& ti, lt::file_index_t i) {
    const lt::file_storage& fs = ti.files();
    if (ti.num_files() <= 1) return ti.name();
    std::string p = fs.file_path(i);
    auto slash = p.find('/');
    if (slash != std::string::npos) return p.substr(slash + 1);
    return p;
}

static json result_ok(std::uint64_t id, const lt::torrent_info& ti, int max_files) {
    json files = json::array();
    const lt::file_storage& fs = ti.files();
    int keep = ti.num_files();
    if (max_files > 0 && keep > max_files) keep = max_files;
    for (int i = 0; i < keep; i++) {
        lt::file_index_t fi{i};
        files.push_back({{"path", record_path(ti, fi)},
                         {"size", static_cast<std::int64_t>(fs.file_size(fi))}});
    }
    return {{"type", "result"},
            {"id", id},
            {"outcome", "ok"},
            {"name", ti.name()},
            {"total_size", static_cast<std::int64_t>(ti.total_size())},
            {"file_count", ti.num_files()},
            {"files", files}};
}

static json result_error(std::uint64_t id, const std::string& what) {
    return {{"type", "result"}, {"id", id}, {"outcome", "error"}, {"error", what}};
}

static json result_timeout(std::uint64_t id, int peers) {
    return {{"type", "result"}, {"id", id}, {"outcome", "timeout"}, {"peers", peers}};
}

// Parse one peer hint — the "host:port" shapes netip.AddrPort.String()
// produces on the Go side ("1.2.3.4:6881", "[2001:db8::1]:6881").
// A hint is a warm start, never a requirement: anything malformed is
// dropped here and costs the job nothing.
static bool parse_peer_hint(const std::string& s, lt::tcp::endpoint& out) {
    std::string host;
    std::string port_str;
    if (!s.empty() && s.front() == '[') {
        auto close = s.find(']');
        if (close == std::string::npos || close + 1 >= s.size() || s[close + 1] != ':')
            return false;
        host = s.substr(1, close - 1);
        port_str = s.substr(close + 2);
    } else {
        auto colon = s.rfind(':');
        if (colon == std::string::npos) return false;
        host = s.substr(0, colon);
        port_str = s.substr(colon + 1);
    }
    if (host.empty() || port_str.empty()) return false;
    char* end = nullptr;
    long port = std::strtol(port_str.c_str(), &end, 10);
    if (end == nullptr || *end != '\0' || port <= 0 || port > 65535) return false;
    lt::error_code ec;
    lt::address addr = lt::make_address(host, ec);
    if (ec) return false;
    out = lt::tcp::endpoint(addr, static_cast<unsigned short>(port));
    return true;
}

// ---------------------------------------------------------------------------

int main(int argc, char** argv) {
    int max_active = 1024;
    std::string save_path;
    for (int i = 1; i < argc; i++) {
        std::string a = argv[i];
        if (a == "--max-active" && i + 1 < argc) {
            max_active = std::atoi(argv[++i]);
        } else if (a == "--save-path" && i + 1 < argc) {
            save_path = argv[++i];
        } else if (a == "--version") {
            std::printf("dhtsearch-lt-sidecar %s (libtorrent %s)\n", kSidecarVersion,
                        LIBTORRENT_VERSION);
            return 0;
        }
    }
    if (save_path.empty()) {
        const char* tmp = std::getenv("TMPDIR");
        save_path = std::string(tmp && *tmp ? tmp : "/tmp") + "/dhtsearch-lt-sidecar";
    }
    // The scratch dir only ever holds libtorrent bookkeeping (upload
    // mode means no payload); create it best-effort.
    std::string mk = "mkdir -p '" + save_path + "'";
    if (std::system(mk.c_str()) != 0) {
        log_line("lt-sidecar: warning: could not create save path " + save_path);
    }

    lt::settings_pack pack;
    pack.set_bool(lt::settings_pack::enable_dht, true);
    // The standard public DHT bootstrap routers, named explicitly so
    // the session's peer supply never silently depends on a default
    // changing under it. The DHT is used read-only in effect — it
    // supplies get_peers for added torrents; upload_mode (below)
    // still means no payload is ever served.
    pack.set_str(lt::settings_pack::dht_bootstrap_nodes,
                 "router.bittorrent.com:6881,router.utorrent.com:6881,"
                 "dht.transmissionbt.com:6881,dht.libtorrent.org:25401");
    pack.set_bool(lt::settings_pack::enable_lsd, false);
    pack.set_bool(lt::settings_pack::enable_upnp, false);
    pack.set_bool(lt::settings_pack::enable_natpmp, false);
    // Port 0 = OS-assigned listen ports (TCP + the UDP socket the
    // DHT shares): zero collision surface with the crawler's DHT
    // ports or the API/web ports by construction.
    pack.set_str(lt::settings_pack::listen_interfaces, "0.0.0.0:0,[::]:0");
    pack.set_int(lt::settings_pack::connections_limit, 4000);
    pack.set_int(lt::settings_pack::alert_mask,
                 lt::alert_category::status | lt::alert_category::error |
                     lt::alert_category::stats);

    // No explicit start_dht() call: it is deprecated (and compiled
    // out at this ABI), because enable_dht in the settings pack is
    // the whole switch — the session starts its DHT from it.
    lt::session ses{lt::session_params(pack)};

    // session_handle::status() is deprecated and compiled out of
    // this build (deprecated-functions=OFF); the DHT routing-table
    // size comes from the session-stats counters instead, requested
    // with post_session_stats() and read out of session_stats_alert.
    const int dht_nodes_metric = lt::find_metric_idx("dht.dht_nodes");

    std::thread reader(reader_thread);

    send_frame({{"type", "ready"},
                {"sidecar", kSidecarVersion},
                {"libtorrent", LIBTORRENT_VERSION}});
    log_line(std::string("lt-sidecar: ready (libtorrent ") + LIBTORRENT_VERSION + ")");

    std::map<lt::sha1_hash, Job> jobs; // keyed by v1 infohash
    auto find_by_handle = [&](const lt::torrent_handle& h) -> std::map<lt::sha1_hash, Job>::iterator {
        return jobs.find(h.info_hashes().get(lt::protocol_version::V1));
    };
    auto drop_job = [&](std::map<lt::sha1_hash, Job>::iterator it, bool delete_files) {
        if (it->second.handle.is_valid()) {
            lt::remove_flags_t opts =
                delete_files ? lt::session::delete_files : lt::remove_flags_t{};
            ses.remove_torrent(it->second.handle, opts);
        }
        jobs.erase(it);
    };

    bool shutdown = false;
    auto last_poll = clock_type::now();
    auto last_dht_poll = clock_type::now();
    bool dht_reported = false;
    bool last_dht_running = false;
    int last_dht_nodes = 0;
    int dht_nodes_latest = 0; // updated by session_stats_alert
    while (!shutdown) {
        if (g_in.eof) break; // parent went away

        // --- inbound messages ---
        std::deque<json> msgs;
        {
            std::lock_guard<std::mutex> lk(g_in.mu);
            msgs.swap(g_in.queue);
        }
        for (const json& msg : msgs) {
            std::string type = msg.value("type", "");
            if (type == "shutdown") {
                shutdown = true;
                break;
            }
            if (type == "cancel") {
                std::uint64_t id = msg.value("id", std::uint64_t{0});
                for (auto it = jobs.begin(); it != jobs.end(); ++it) {
                    if (it->second.id == id) {
                        drop_job(it, true);
                        break;
                    }
                }
                continue;
            }
            if (type != "fetch") continue;

            std::uint64_t id = msg.value("id", std::uint64_t{0});
            std::string magnet = msg.value("magnet", "");
            long long deadline_ms = msg.value("deadline_ms", 45000);
            int job_max_files = msg.value("max_files", 50);
            if (static_cast<int>(jobs.size()) >= max_active) {
                send_frame(result_error(id, "sidecar busy: too many active torrents"));
                continue;
            }
            lt::error_code ec;
            lt::add_torrent_params p = lt::parse_magnet_uri(magnet, ec);
            if (ec) {
                send_frame(result_error(id, "parse magnet: " + ec.message()));
                continue;
            }
            p.save_path = save_path;
            // Metadata-only: never request pieces (upload_mode), and
            // keep the session from lifting the torrent out of
            // upload mode on its own (auto_managed). duplicate_is_error
            // makes a second concurrent add of the same hash fail
            // instead of silently sharing the torrent. paused must be
            // cleared explicitly: libtorrent 2.x's default add flags
            // include it, and with auto_managed gone nothing would
            // ever resume the torrent (it would sit in "dl metadata"
            // forever, announcing to nobody).
            p.flags |= lt::torrent_flags::upload_mode;
            p.flags |= lt::torrent_flags::duplicate_is_error;
            p.flags &= ~lt::torrent_flags::auto_managed;
            p.flags &= ~lt::torrent_flags::paused;
            lt::torrent_handle h = ses.add_torrent(p, ec);
            if (ec) {
                send_frame(result_error(id, "add torrent: " + ec.message()));
                continue;
            }
            // Peer hints (optional, protocol extension): addresses
            // the Go side's peer-hint cache already knows are in
            // this swarm. Connect them alongside the tracker/PEX/DHT
            // supply; a malformed or dead hint is ignored by design.
            if (msg.contains("peer_hints") && msg["peer_hints"].is_array()) {
                for (const auto& hint : msg["peer_hints"]) {
                    if (!hint.is_string()) continue;
                    lt::tcp::endpoint ep;
                    if (parse_peer_hint(hint.get<std::string>(), ep)) {
                        h.connect_peer(ep);
                    }
                }
            }
            Job job;
            job.id = id;
            job.handle = h;
            job.deadline = clock_type::now() + std::chrono::milliseconds(deadline_ms);
            job.max_files = job_max_files;
            jobs[h.info_hashes().get(lt::protocol_version::V1)] = job;
        }
        if (shutdown) break;

        // --- alerts ---
        ses.wait_for_alert(std::chrono::milliseconds(20));
        std::vector<lt::alert*> alerts;
        ses.pop_alerts(&alerts);
        for (lt::alert* a : alerts) {
            if (auto* mra = lt::alert_cast<lt::metadata_received_alert>(a)) {
                auto it = find_by_handle(mra->handle);
                if (it == jobs.end()) continue;
                std::shared_ptr<const lt::torrent_info> ti = mra->handle.torrent_file();
                std::uint64_t id = it->second.id;
                int mf = it->second.max_files;
                drop_job(it, true);
                if (ti) {
                    send_frame(result_ok(id, *ti, mf));
                } else {
                    send_frame(result_error(id, "metadata received but torrent_info unavailable"));
                }
            } else if (auto* mfa = lt::alert_cast<lt::metadata_failed_alert>(a)) {
                auto it = find_by_handle(mfa->handle);
                if (it == jobs.end()) continue;
                std::uint64_t id = it->second.id;
                std::string what = "metadata failed: " + mfa->error.message();
                drop_job(it, true);
                send_frame(result_error(id, what));
            } else if (auto* tea = lt::alert_cast<lt::torrent_error_alert>(a)) {
                auto it = find_by_handle(tea->handle);
                if (it == jobs.end()) continue;
                std::uint64_t id = it->second.id;
                std::string what = "torrent error: " + tea->error.message();
                drop_job(it, true);
                send_frame(result_error(id, what));
            } else if (auto* ssa = lt::alert_cast<lt::session_stats_alert>(a)) {
                if (dht_nodes_metric >= 0) {
                    auto counters = ssa->counters();
                    if (dht_nodes_metric < static_cast<int>(counters.size())) {
                        dht_nodes_latest =
                            static_cast<int>(counters[static_cast<std::size_t>(dht_nodes_metric)]);
                    }
                }
            }
            // Everything else (add_torrent, state changes, tracker
            // replies, listen sockets...) is bookkeeping we don't
            // need: sync add_torrent above already reports failures.
        }

        // --- peer counts + deadlines, ~every 100ms ---
        auto now = clock_type::now();
        if (now - last_poll >= std::chrono::milliseconds(100)) {
            last_poll = now;
            for (auto& kv : jobs) {
                Job& job = kv.second;
                if (!job.handle.is_valid()) continue;
                lt::torrent_status st = job.handle.status();
                int prev = job.last_peers;
                job.last_peers = st.num_peers;
                if (st.num_peers > 0 && (!job.peer_reported || st.num_peers != prev)) {
                    job.peer_reported = true;
                    send_frame({{"type", "peers"}, {"id", job.id}, {"peers", st.num_peers}});
                }
            }
            // Deadline expiry pass (collect first, then drop).
            std::vector<lt::sha1_hash> expired;
            for (auto& kv : jobs) {
                if (now >= kv.second.deadline) expired.push_back(kv.first);
            }
            for (const lt::sha1_hash& ih : expired) {
                auto it = jobs.find(ih);
                if (it == jobs.end()) continue;
                std::uint64_t id = it->second.id;
                int peers = it->second.last_peers;
                drop_job(it, true);
                send_frame(result_timeout(id, peers));
            }
        }

        // --- DHT status, ~every 1s, sent on change ---
        // The Go side surfaces this in the admin's fetch section:
        // running + routing-table size is the visible proof the
        // DHT peer supply is alive (and a table pinned at zero is
        // how a UDP-blocked host shows up). The node count lags one
        // cycle: it is whatever the latest session_stats_alert
        // carried, refreshed by the post below.
        if (now - last_dht_poll >= std::chrono::seconds(1)) {
            last_dht_poll = now;
            bool running = ses.is_dht_running();
            if (running) ses.post_session_stats();
            int nodes = running ? dht_nodes_latest : 0;
            if (!dht_reported || running != last_dht_running || nodes != last_dht_nodes) {
                dht_reported = true;
                last_dht_running = running;
                last_dht_nodes = nodes;
                send_frame({{"type", "status"},
                            {"dht_running", running},
                            {"dht_nodes", nodes}});
            }
        }
    }

    log_line("lt-sidecar: shutting down");
    reader.detach();
    return 0;
}
