package collector

import (
	"encoding/json"
	"testing"
)

func TestNormalizeDiskDevice(t *testing.T) {
	cases := map[string]string{
		"/dev/sda":        "sda",
		"/dev/sda1":       "sda",
		"/dev/vda2":       "vda",
		"/dev/nvme0n1":    "nvme0n1",
		"/dev/nvme0n1p2":  "nvme0n1",
		"/dev/mmcblk0":    "mmcblk0",
		"/dev/mmcblk0p1":  "mmcblk0",
		"/dev/dm-0":       "dm-0",
		"/dev/md126":      "md126",
		"/dev/mapper/foo": "mapper/foo",
	}
	for in, want := range cases {
		if got := normalizeDiskDevice(in); got != want {
			t.Errorf("normalizeDiskDevice(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiskJSONIOPercentContract(t *testing.T) {
	// io_percent 尽力而为可空：无值保留键且为 null；有值序列化为数值。
	d := DiskInfo{Disks: []DiskItem{{Index: 0, Name: "sda", Mount: "/", Total: 1, Percent: 50}}}
	data, _ := json.Marshal(d)
	var m1 map[string]any
	_ = json.Unmarshal(data, &m1)
	disks := m1["disks"].([]any)
	first := disks[0].(map[string]any)
	if v, ok := first["io_percent"]; !ok || v != nil {
		t.Errorf("无 io_percent 时应保留键且为 null, got %v (present=%v)", v, ok)
	}

	v := 23.5
	d.Disks[0].IoPercent = &v
	data, _ = json.Marshal(d)
	var m2 map[string]any
	_ = json.Unmarshal(data, &m2)
	first2 := m2["disks"].([]any)[0].(map[string]any)
	if first2["io_percent"].(float64) != 23.5 {
		t.Errorf("io_percent 序列化不符: %v", first2["io_percent"])
	}
}
