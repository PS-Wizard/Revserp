package localvisibility

import (
	"math"
	"testing"
)

func greatCircleDistanceM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = earthRadiusM
	phi1 := lat1 * math.Pi / 180
	phi2 := lat2 * math.Pi / 180
	dPhi := (lat2 - lat1) * math.Pi / 180
	dLambda := normalizeDelta(lon2-lon1) * math.Pi / 180
	a := math.Sin(dPhi/2)*math.Sin(dPhi/2) +
		math.Cos(phi1)*math.Cos(phi2)*math.Sin(dLambda/2)*math.Sin(dLambda/2)
	return r * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func initialBearingDeg(lat1, lon1, lat2, lon2 float64) float64 {
	phi1 := lat1 * math.Pi / 180
	phi2 := lat2 * math.Pi / 180
	dLambda := normalizeDelta(lon2-lon1) * math.Pi / 180
	y := math.Sin(dLambda) * math.Cos(phi2)
	x := math.Cos(phi1)*math.Sin(phi2) - math.Sin(phi1)*math.Cos(phi2)*math.Cos(dLambda)
	return math.Atan2(y, x) * 180 / math.Pi
}

func normalizeDelta(delta float64) float64 {
	delta = math.Mod(delta+180, 360)
	if delta < 0 {
		delta += 360
	}
	return delta - 180
}

func within(t *testing.T, name string, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Errorf("%s = %v, want %v (tolerance %v)", name, got, want, tolerance)
	}
}

func TestExpectedRunCredits(t *testing.T) {
	credits, err := ExpectedRunCredits(MapQueryCount, GridPointCount)
	if err != nil {
		t.Fatalf("ExpectedRunCredits(%d,%d) returned error: %v", MapQueryCount, GridPointCount, err)
	}
	// A thin geography yields a smaller honest run and pays less, because cost
	// derives from the actual count rather than a padded constant.
	thin, err := ExpectedRunCredits(3, GridPointCount)
	if err != nil {
		t.Fatalf("ExpectedRunCredits(3,%d) returned error: %v", GridPointCount, err)
	}
	if thin != 3*GridPointCount*MapsCreditsPerCall {
		t.Fatalf("thin credits = %d, want 3 x points x per-call", thin)
	}
	if want := MapQueryCount * GridPointCount * MapsCreditsPerCall; credits != want {
		t.Fatalf("credits = %d, want %d (query count x point count x per call)", credits, want)
	}
	if credits != 135 {
		t.Fatalf("credits = %d, want 135 for the fixed five queries and nine points", credits)
	}

	for _, tc := range []struct {
		name              string
		queryCount, point int
	}{
		{"zero queries", 0, GridPointCount},
		{"too many queries", MapQueryCount + 1, GridPointCount},
		{"too few points", MapQueryCount, GridPointCount - 1},
		{"too many points", MapQueryCount, GridPointCount + 1},
		{"both wrong", 0, GridPointCount + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ExpectedRunCredits(tc.queryCount, tc.point); err == nil {
				t.Fatalf("ExpectedRunCredits(%d,%d) = %d, want error", tc.queryCount, tc.point, got)
			}
		})
	}
}

func TestBuildGeoGridValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lat, lon float64
		radiusM  int
	}{
		{"latitude above range", 90.0001, 0, 5000},
		{"latitude below range", -90.0001, 0, 5000},
		{"longitude above range", 0, 180.0001, 5000},
		{"longitude below range", 0, -180.0001, 5000},
		{"latitude NaN", math.NaN(), 0, 5000},
		{"longitude NaN", 0, math.NaN(), 5000},
		{"latitude +Inf", math.Inf(1), 0, 5000},
		{"longitude -Inf", 0, math.Inf(-1), 5000},
		{"radius below range", 0, 0, 999},
		{"radius above range", 0, 0, 25001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := BuildGeoGrid(tc.lat, tc.lon, tc.radiusM); err == nil {
				t.Fatalf("BuildGeoGrid(%v,%v,%d) = %d points, want error", tc.lat, tc.lon, tc.radiusM, len(got))
			}
		})
	}

	for _, tc := range []struct {
		name    string
		radiusM int
	}{
		{"radius minimum", 1000},
		{"radius maximum", 25000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildGeoGrid(0, 0, tc.radiusM); err != nil {
				t.Fatalf("BuildGeoGrid radius %d returned error: %v", tc.radiusM, err)
			}
		})
	}
}

func TestBuildGeoGridLayout(t *testing.T) {
	const (
		lat     = 40.0
		lon     = -74.0
		radiusM = 5000
	)
	points, err := BuildGeoGrid(lat, lon, radiusM)
	if err != nil {
		t.Fatalf("BuildGeoGrid returned error: %v", err)
	}
	if len(points) != GridPointCount {
		t.Fatalf("len(points) = %d, want %d", len(points), GridPointCount)
	}

	wantSectors := []string{"NW", "N", "NE", "W", "centre", "E", "SW", "S", "SE"}
	wantRings := []string{"corner", "edge", "corner", "edge", "centre", "edge", "corner", "edge", "corner"}
	wantBearings := []float64{-45, 0, 45, -90, 0, 90, -135, 180, 135}

	halfWidth := float64(radiusM) / math.Sqrt2
	wantDistances := []float64{
		radiusM, halfWidth, radiusM,
		halfWidth, 0, halfWidth,
		radiusM, halfWidth, radiusM,
	}

	for i, p := range points {
		if p.PointIndex != i {
			t.Errorf("points[%d].PointIndex = %d, want %d", i, p.PointIndex, i)
		}
		if p.Sector != wantSectors[i] {
			t.Errorf("points[%d].Sector = %q, want %q", i, p.Sector, wantSectors[i])
		}
		if p.Ring != wantRings[i] {
			t.Errorf("points[%d].Ring = %q, want %q", i, p.Ring, wantRings[i])
		}
		within(t, p.Sector+" distance_m", p.DistanceM, wantDistances[i], 1e-9)
		within(t, p.Sector+" haversine distance_m", greatCircleDistanceM(lat, lon, p.Latitude, p.Longitude), wantDistances[i], 1e-6)
		if i == 4 {
			continue
		}
		gotBearing := initialBearingDeg(lat, lon, p.Latitude, p.Longitude)
		within(t, p.Sector+" bearing", normalizeDelta(gotBearing-wantBearings[i]), 0, 1e-9)
	}

	for col := 0; col < 3; col++ {
		north := points[col].Latitude
		centre := points[3+col].Latitude
		south := points[6+col].Latitude
		if !(north > centre && centre > south) {
			t.Errorf("column %d latitude order north>centre>south violated: %v, %v, %v", col, north, centre, south)
		}
	}
	for row := 0; row < 3; row++ {
		west := points[row*3].Longitude
		centre := points[row*3+1].Longitude
		east := points[row*3+2].Longitude
		if !(west < centre && centre < east) {
			t.Errorf("row %d longitude order west<centre<east violated: %v, %v, %v", row, west, centre, east)
		}
	}

	within(t, "centre latitude", points[4].Latitude, lat, 1e-12)
	within(t, "centre longitude", points[4].Longitude, lon, 1e-12)
}

func TestBuildGeoGridHighLatitude(t *testing.T) {
	const (
		lat     = 80.0
		lon     = 10.0
		radiusM = 10000
	)
	points, err := BuildGeoGrid(lat, lon, radiusM)
	if err != nil {
		t.Fatalf("BuildGeoGrid returned error: %v", err)
	}
	halfWidth := float64(radiusM) / math.Sqrt2
	wantDistances := []float64{
		radiusM, halfWidth, radiusM,
		halfWidth, 0, halfWidth,
		radiusM, halfWidth, radiusM,
	}
	for i, p := range points {
		within(t, p.Sector+" haversine distance_m", greatCircleDistanceM(lat, lon, p.Latitude, p.Longitude), wantDistances[i], 1e-6)
		if p.Longitude < -180 || p.Longitude > 180 {
			t.Errorf("points[%d].Longitude = %v, want within [-180,180]", i, p.Longitude)
		}
	}
	if east := points[5].Longitude - lon; east < 0.05 {
		t.Errorf("east longitude span at latitude %v = %v, want a large span", lat, east)
	}
}

func TestBuildGeoGridDateline(t *testing.T) {
	const (
		lat     = 0.0
		lon     = 179.99
		radiusM = 5000
	)
	points, err := BuildGeoGrid(lat, lon, radiusM)
	if err != nil {
		t.Fatalf("BuildGeoGrid returned error: %v", err)
	}
	crossed := false
	for i, p := range points {
		if p.Longitude < -180 || p.Longitude > 180 {
			t.Errorf("points[%d].Longitude = %v, want normalised within [-180,180]", i, p.Longitude)
		}
		if p.Longitude < 0 {
			crossed = true
		}
	}
	if !crossed {
		t.Error("expected east points to be normalised past the dateline")
	}
	halfWidth := float64(radiusM) / math.Sqrt2
	wantDistances := []float64{
		radiusM, halfWidth, radiusM,
		halfWidth, 0, halfWidth,
		radiusM, halfWidth, radiusM,
	}
	for i, p := range points {
		within(t, p.Sector+" haversine distance_m", greatCircleDistanceM(lat, lon, p.Latitude, p.Longitude), wantDistances[i], 1e-6)
	}
	if points[5].Longitude > -179.9 || points[5].Longitude < -180 {
		t.Errorf("east edge longitude = %v, want a small negative wrap", points[5].Longitude)
	}
}

func TestBuildGeoGridSouthernHemisphere(t *testing.T) {
	const (
		lat     = -33.0
		lon     = 151.0
		radiusM = 25000
	)
	points, err := BuildGeoGrid(lat, lon, radiusM)
	if err != nil {
		t.Fatalf("BuildGeoGrid returned error: %v", err)
	}
	if !(points[0].Latitude > points[4].Latitude && points[4].Latitude > points[6].Latitude) {
		t.Errorf("latitude order north>centre>south violated in southern hemisphere")
	}
	within(t, "corner distance_m", greatCircleDistanceM(lat, lon, points[0].Latitude, points[0].Longitude), float64(radiusM), 1e-6)
}
