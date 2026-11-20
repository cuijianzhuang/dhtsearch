package settings

// Preset is a named bundle of canonical values for one settings
// group's form. Applying a preset in the UI only fills the form
// fields — nothing is saved until the operator presses 保存设置，
// and list-valued settings (the operator's own word/domain lists)
// are never part of a preset, so a preset can never erase them.
type Preset struct {
	Name   string            `json:"name"`
	Label  string            `json:"label"`
	Desc   string            `json:"desc,omitempty"`
	Values map[string]string `json:"values"`
}

// FilterPresets are the content-filter templates offered on the
// filter settings page. They cover the filter group's switches and
// thresholds only; MIN_TORRENT_SIZE (the size floor's value) lives
// in the metadata group and is deliberately not touched — the
// too-small switch here is what turns that floor on or off.
//
// 标准 is the registry defaults verbatim. 宽松/严格 spread from
// them along each rule's own semantics (see filter.Check):
//   - MaxNameLen: names are spam past this many runes — 2× looser /
//     ½ tighter.
//   - StuffingMinRepeat: one word repeated this often is stuffing —
//     2× looser / the registry minimum (2) tighter.
//   - NonAlnumMaxRatio: symbol share of the name — headroom up when
//     loose, down when strict.
//   - RandomFileMaxRatio: random-looking file names share — 2× / ½.
//   - TooSmall: loose turns the size floor off entirely (the floor
//     mostly drops legitimate small releases); strict keeps it on.
//
// 成人模式 is 标准 with the adult category switched off: adult
// torrents are then indexed and publicly searchable like any other
// content, while the spam and size rules keep their 标准 values.
var FilterPresets = []Preset{
	{
		Name:  "loose",
		Label: "宽松",
		Desc:  "成人与垃圾判定放宽，体积过滤关闭：只有明显的问题内容才被丢弃",
		Values: map[string]string{
			KeyFilterEnabled:            "true",
			KeyFilterAdultEnabled:       "true",
			KeyFilterSpamEnabled:        "true",
			KeyFilterTooSmallEnabled:    "false",
			KeyFilterMaxNameLen:         "600",
			KeyFilterStuffingMinRepeat:  "8",
			KeyFilterNonAlnumMaxRatio:   "0.7",
			KeyFilterRandomFileMaxRatio: "0.6",
		},
	},
	{
		Name:  "standard",
		Label: "标准",
		Desc:  "系统默认值：三类过滤全开，阈值取内置缺省",
		Values: map[string]string{
			KeyFilterEnabled:            "true",
			KeyFilterAdultEnabled:       "true",
			KeyFilterSpamEnabled:        "true",
			KeyFilterTooSmallEnabled:    "true",
			KeyFilterMaxNameLen:         "300",
			KeyFilterStuffingMinRepeat:  "4",
			KeyFilterNonAlnumMaxRatio:   "0.5",
			KeyFilterRandomFileMaxRatio: "0.3",
		},
	},
	{
		Name:  "strict",
		Label: "严格",
		Desc:  "三类过滤全开且阈值收紧：更短的名字上限、更低的堆砌与占比容忍",
		Values: map[string]string{
			KeyFilterEnabled:            "true",
			KeyFilterAdultEnabled:       "true",
			KeyFilterSpamEnabled:        "true",
			KeyFilterTooSmallEnabled:    "true",
			KeyFilterMaxNameLen:         "150",
			KeyFilterStuffingMinRepeat:  "2",
			KeyFilterNonAlnumMaxRatio:   "0.3",
			KeyFilterRandomFileMaxRatio: "0.15",
		},
	},
	{
		Name:  "adult",
		Label: "成人模式",
		Desc:  "成人过滤关闭（成人内容直接入库），垃圾与体积过滤保持标准",
		Values: map[string]string{
			KeyFilterEnabled:            "true",
			KeyFilterAdultEnabled:       "false",
			KeyFilterSpamEnabled:        "true",
			KeyFilterTooSmallEnabled:    "true",
			KeyFilterMaxNameLen:         "300",
			KeyFilterStuffingMinRepeat:  "4",
			KeyFilterNonAlnumMaxRatio:   "0.5",
			KeyFilterRandomFileMaxRatio: "0.3",
		},
	},
}
