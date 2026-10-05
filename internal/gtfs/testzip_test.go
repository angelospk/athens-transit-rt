package gtfs

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// writeZip builds a GTFS zip from file name -> CSV content and returns its path.
func writeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gtfs.zip")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// sampleFiles is a tiny network: line 040 (route 938, bus) with two trips on one shape,
// one trip of line 2 (trolley) running past midnight, and a calendar exception.
func sampleFiles() map[string]string {
	return map[string]string{
		"stops.txt": "\xef\xbb\xbfstop_id,stop_code,stop_name,stop_lat,stop_lon\n" +
			"A,A,ΠΕΙΡΑΙΑΣ,37.9400,23.6400\n" +
			"B,B,ΜΕΣΗ,37.9500,23.6500\n" +
			"C,C,ΣΥΝΤΑΓΜΑ,37.9600,23.6600\n",
		"routes.txt": "route_id,route_short_name,route_long_name,route_type,route_color,route_text_color\n" +
			"938,040,ΠΕΙΡΑΙΑΣ - ΣΥΝΤΑΓΜΑ,3,153CE0,FFFFFF\n" +
			"1070,2,ΑΝΩ ΚΥΨΕΛΗ - ΚΑΙΣΑΡΙΑΝΗ,11,F19A1E,000000\n",
		"trips.txt": "route_id,service_id,trip_id,trip_headsign,direction_id,shape_id\n" +
			"938,day_1,t1,ΣΥΝΤΑΓΜΑ ,0,S1\n" +
			"938,day_1,t2,ΣΥΝΤΑΓΜΑ ,0,S1\n" +
			"1070,day_1,n1,ΚΑΙΣΑΡΙΑΝΗ,1,S1\n" +
			"938,day_1,lonely,X,0,S1\n",
		// t1 rows out of order; n1 runs past midnight; lonely has a single stop and is dropped.
		"stop_times.txt": "trip_id,arrival_time,departure_time,stop_id,stop_sequence,pickup_type,drop_off_type\n" +
			"t1,10:10:00,10:10:00,C,3,0,0\n" +
			"t1,10:00:00,10:01:00,A,1,0,0\n" +
			"t1,10:05:00,10:05:00,B,2,0,0\n" +
			"t2,11:00:00,11:01:00,A,1,0,0\n" +
			"t2,11:05:00,11:05:00,B,2,0,0\n" +
			"t2,11:10:00,11:10:00,C,3,0,0\n" +
			"n1,24:20:00,24:20:00,A,1,0,0\n" +
			"n1,24:40:00,24:40:00,C,2,0,0\n" +
			"lonely,09:00:00,09:00:00,A,1,0,0\n",
		"shapes.txt": "shape_id,shape_pt_lat,shape_pt_lon,shape_pt_sequence,shape_dist_traveled\n" +
			"S1,37.9600,23.6600,30,0\n" +
			"S1,37.9400,23.6400,1,0\n" +
			"S1,37.9500,23.6500,10,0\n",
		"calendar.txt": "service_id,monday,tuesday,wednesday,thursday,friday,saturday,sunday,start_date,end_date\n" +
			"day_1,1,0,0,0,0,0,0,20260706,20261006\n",
		"calendar_dates.txt": "service_id,date,exception_type\n" +
			"day_1,20260713,2\n" +
			"day_1,20260715,1\n",
	}
}
