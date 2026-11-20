cmake_minimum_required(VERSION 3.16)
project(dhtsearch-lt-sidecar CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

if(NOT CMAKE_BUILD_TYPE)
    set(CMAKE_BUILD_TYPE Release)
endif()

# LibtorrentRasterbar's config package is produced both by the
# distro package (libtorrent-rasterbar-dev) and by the from-source
# static install that build.sh performs.
find_package(LibtorrentRasterbar 2.0 REQUIRED)
find_package(nlohmann_json 3 REQUIRED)
find_package(Threads REQUIRED)

add_executable(dhtsearch-lt-sidecar src/main.cpp)
# libtorrent's CMake build compiles the library at ABI version 3
# (symbols under the libtorrent::v2 inline namespace, via
# TORRENT_NO_DEPRECATE); consumers must select the same ABI or
# nothing links. build.sh pins the library side to the same value.
target_compile_definitions(dhtsearch-lt-sidecar PRIVATE TORRENT_ABI_VERSION=3)
target_link_libraries(dhtsearch-lt-sidecar PRIVATE
    LibtorrentRasterbar::torrent-rasterbar
    nlohmann_json::nlohmann_json
    Threads::Threads)

# Portability of the shipped binary: the only dynamic dependencies we
# accept are the target distro's glibc (the build container is Debian
# bookworm, glibc 2.36 — the floor for Debian 12/13 hosts). libstdc++
# and libgcc are linked statically so a newer/older libstdc++ on the
# host cannot break the sidecar.
target_link_options(dhtsearch-lt-sidecar PRIVATE -static-libstdc++ -static-libgcc)

install(TARGETS dhtsearch-lt-sidecar RUNTIME DESTINATION bin)
