package shealth

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// a download in the shape Samsung Health writes: a preamble line, the prefixed header, the rows
func writeZip(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "samsunghealth_vlad_202610120300.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	add("samsunghealth_vlad_202610120300/com.samsung.shealth.tracker.pedometer_day_summary.202610120300.csv",
		"com.samsung.shealth.tracker.pedometer_day_summary,10001,1\n"+
			"com.samsung.shealth.tracker.pedometer_day_summary.step_count,com.samsung.shealth.tracker.pedometer_day_summary.distance,com.samsung.shealth.tracker.pedometer_day_summary.calorie,com.samsung.shealth.tracker.pedometer_day_summary.day_time,com.samsung.shealth.tracker.pedometer_day_summary.source_type,com.samsung.shealth.tracker.pedometer_day_summary.datauuid,time_offset\n"+
			"15909,13270.5,964.2,1791676800000,-2,aa,UTC+0200\n"+ // 2026-10-11 00:00 UTC + 2h -> 2026-10-11
			"18776,13270.5,964.2,1791676800000,1,bb,UTC+0200\n"+ // the watch's row of the same day: the larger count wins
			"12032,8950,561,1791763200000,-2,cc,UTC+0200\n")
	add("samsunghealth_vlad_202610120300/com.samsung.shealth.sleep.202610120300.csv",
		"com.samsung.shealth.sleep,10001,1\n"+
			"com.samsung.health.sleep.start_time,com.samsung.health.sleep.end_time,com.samsung.health.sleep.time_offset,com.samsung.health.sleep.datauuid\n"+
			"2026-10-10 21:30:00.000,2026-10-11 05:00:00.000,UTC+0200,s1\n")
	add("samsunghealth_vlad_202610120300/com.samsung.health.sleep_stage.202610120300.csv",
		"com.samsung.health.sleep_stage,10001,1\n"+
			"com.samsung.health.sleep_stage.start_time,com.samsung.health.sleep_stage.end_time,com.samsung.health.sleep_stage.stage,com.samsung.health.sleep_stage.sleep_id,com.samsung.health.sleep_stage.time_offset\n"+
			"2026-10-10 21:30:00.000,2026-10-10 22:30:00.000,40002,s1,UTC+0200\n"+
			"2026-10-10 22:30:00.000,2026-10-11 00:30:00.000,40003,s1,UTC+0200\n"+
			"2026-10-11 00:30:00.000,2026-10-11 04:30:00.000,40004,s1,UTC+0200\n"+
			"2026-10-11 04:30:00.000,2026-10-11 05:00:00.000,40001,s1,UTC+0200\n")
	// the real export's shape: a byte order mark, plain column names, a trailing comma, and
	// the per-minute readings in a binning file the row names
	add("samsunghealth_vlad_202610120300/com.samsung.shealth.tracker.heart_rate.202610120300.csv",
		"\ufeffcom.samsung.shealth.tracker.heart_rate,7006011,3\n"+
			"create_sh_ver,heart_rate,start_time,end_time,binning_data,time_offset,deviceuuid,pkg_name,datauuid\n"+
			"63001451,60,2026-10-11 01:00:00.000,2026-10-11 01:10:00.000,hr1.binning_data.json,UTC+0200,EsTOzxqamY,com.sec.android.app.shealth,hr1,\n"+
			"63001451,134,2026-10-11 16:05:00.000,2026-10-11 16:06:00.000,,UTC+0200,EsTOzxqamY,com.sec.android.app.shealth,hr2,\n")
	add("samsunghealth_vlad_202610120300/jsons/com.samsung.shealth.tracker.heart_rate/hr1.binning_data.json",
		`[{"start_time":1791676800000,"end_time":1791676859999,"heart_rate":52.0,"heart_rate_min":50,"heart_rate_max":54},`+
			`{"start_time":1791676920000,"end_time":1791676979999,"heart_rate":55.0,"heart_rate_min":53,"heart_rate_max":57},`+
			`{"start_time":1791677100000,"end_time":1791677159999,"heart_rate":0.0,"heart_rate_min":0,"heart_rate_max":0}]`)
	add("samsunghealth_vlad_202610120300/com.samsung.shealth.exercise.202610120300.csv",
		"com.samsung.shealth.exercise,10001,1\n"+
			"com.samsung.health.exercise.start_time,com.samsung.health.exercise.duration,com.samsung.health.exercise.time_offset\n"+
			"2026-10-11 10:00:00.000,2700000,UTC+0200\n")
	add("samsunghealth_vlad_202610120300/com.samsung.health.weight.202610120300.csv",
		"com.samsung.health.weight,10001,1\n"+
			"com.samsung.health.weight.weight,com.samsung.health.weight.body_fat,com.samsung.health.weight.start_time,com.samsung.health.weight.time_offset\n"+
			"80.4,18.5,2026-10-11 07:00:00.000,UTC+0200\n")
	add("samsunghealth_vlad_202610120300/com.samsung.shealth.stress.202610120300.csv",
		"com.samsung.shealth.stress,10001,1\n"+
			"com.samsung.shealth.stress.score,com.samsung.shealth.stress.start_time,com.samsung.shealth.stress.time_offset\n"+
			"30,2026-10-11 09:00:00.000,UTC+0200\n"+
			"50,2026-10-11 15:00:00.000,UTC+0200\n")
	add("samsunghealth_vlad_202610120300/com.samsung.shealth.tracker.oxygen_saturation.202610120300.csv",
		"com.samsung.shealth.tracker.oxygen_saturation,10001,1\n"+
			"com.samsung.health.oxygen_saturation.spo2,com.samsung.health.oxygen_saturation.start_time,com.samsung.health.oxygen_saturation.time_offset\n"+
			"97,2026-10-11 02:00:00.000,UTC+0200\n")
	add("samsunghealth_vlad_202610120300/com.samsung.shealth.food_info.202610120300.csv",
		"com.samsung.shealth.food_info,10001,1\n"+
			"com.samsung.shealth.food_info.name,com.samsung.shealth.food_info.create_time\n"+
			"apple,2026-10-11 12:00:00.000\n")
	add("samsunghealth_vlad_202610120300/jsons/readme.txt", "not a table\n")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func TestScanListsEveryTable(t *testing.T) {
	rep, err := Scan(writeZip(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Tables) != 9 {
		t.Fatalf("tables: %d, %+v", len(rep.Tables), rep.Tables)
	}
	by := map[string]Table{}
	for _, tb := range rep.Tables {
		by[tb.Name] = tb
	}
	if by["tracker.pedometer_day_summary"].Rows != 3 || by["tracker.pedometer_day_summary"].From != "2026-10-11" || by["tracker.pedometer_day_summary"].To != "2026-10-12" {
		t.Fatalf("pedometer: %+v", by["tracker.pedometer_day_summary"])
	}
	if by["food_info"].Read != "not read" {
		t.Fatalf("food: %+v", by["food_info"])
	}
	if by["sleep"].From != "2026-10-10" {
		t.Fatalf("sleep span: %+v", by["sleep"])
	}
	if rep.Days != 3 { // 10, 11, 12 October
		t.Fatalf("days: %d", rep.Days)
	}
}

func TestBatchesTakeTheKnownTables(t *testing.T) {
	bs, err := Batches(writeZip(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 1 {
		t.Fatalf("months: %d", len(bs))
	}
	b := bs[0]
	days := map[string]map[string]float64{}
	for _, d := range b.Days {
		days[d.Day] = d.Metrics
	}
	d11 := days["2026-10-11"]
	if d11 == nil {
		t.Fatalf("no 11th: %+v", days)
	}
	want := map[string]float64{
		"steps": 18776, "distance_km": 13.27, "active_calories": 964.2,
		"sleep_minutes": 450, "sleep_light_minutes": 60, "sleep_deep_minutes": 120, "sleep_rem_minutes": 240, "sleep_awake_minutes": 30,
		"hr_avg": 80.33, "hr_min": 52, "hr_max": 134, "exercise_minutes": 45, "weight_kg": 80.4, "body_fat_pct": 18.5, "stress": 40, "spo2_pct": 97,
	}
	for k, v := range want {
		got, ok := d11[k]
		if !ok || got < v-0.01 || got > v+0.01 {
			t.Errorf("%s: got %v want %v", k, got, v)
		}
	}
	if days["2026-10-12"]["steps"] != 12032 {
		t.Errorf("12th: %+v", days["2026-10-12"])
	}
	if len(b.Samples) != 2 || b.Samples[0].Value != 52 || b.Samples[1].Value != 134 {
		t.Errorf("samples: %+v", b.Samples)
	}
}

func TestTheSmallParts(t *testing.T) {
	if tableName("samsunghealth_x/com.samsung.shealth.tracker.heart_rate.202610120312.csv") != "tracker.heart_rate" {
		t.Error(tableName("com.samsung.shealth.tracker.heart_rate.202610120312.csv"))
	}
	if tableName("com.samsung.health.sleep_stage.20261012.csv") != "sleep_stage" {
		t.Error("sleep_stage")
	}
	if offset("UTC+0100") != 3600 || offset("UTC-0530") != -(5*3600+30*60) || offset("") != 0 {
		t.Error("offset")
	}
	if tm, ok := at("1791676800000"); !ok || tm.Format("2006-01-02") != "2026-10-11" {
		t.Error("epoch ms")
	}
	if _, ok := at("not a time"); ok {
		t.Error("garbage")
	}
}
