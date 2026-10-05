// Package localvisibility implements the geometry for Layer 1 local SEO
// visibility: the fixed 3x3 Google Maps grid sampled around a physical
// location, and the expected credit cost of a location run.
package localvisibility

import (
	"errors"
	"fmt"
	"math"
)

const (
	// MapQueryCount caps the queries tracked per location. Geography that can
	// only supply fewer honest queries yields a smaller, cheaper run rather
	// than padded word-order duplicates. A run may carry between one and
	// MapQueryCount queries; cost always derives from the actual count.
	MapQueryCount = 5
	// GridPointCount is the fixed number of grid points sampled per run.
	GridPointCount = 9
	// MapsCreditsPerCall is the Serper Maps credit cost of one provider call.
	MapsCreditsPerCall = 3
	// MapsZoom is the fixed zoom level used for every grid request.
	MapsZoom = 14
)

const earthRadiusM = 6371000.0

// GridPoint is one sampled point of a location's 3x3 measurement grid.
//
// PointIndex is row-major from 0 (north-west) to 8 (south-east); rows run
// north to south and columns west to east. Ring is "centre", "edge", or
// "corner". Sector is "centre", "N", "NE", "E", "SE", "S", "SW", "W", or "NW".
// DistanceM is the planar offset from the location in metres, so corners are
// radius, edge midpoints radius/sqrt(2), and the centre zero.
type GridPoint struct {
	PointIndex int     `json:"point_index"`
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
	DistanceM  float64 `json:"distance_m"`
	Ring       string  `json:"ring"`
	Sector     string  `json:"sector"`
}

// ExpectedRunCredits returns the credits reserved for one location run, the
// exact product of the query count, point count, and per-call credit cost. It
// accepts between one and MapQueryCount queries so a thin geography costs
// only what it actually searches; it still rejects anything above the cap.
func ExpectedRunCredits(queryCount, pointCount int) (int, error) {
	if queryCount < 1 || queryCount > MapQueryCount {
		return 0, fmt.Errorf("expected run credits: query count = %d, want between 1 and %d", queryCount, MapQueryCount)
	}
	if pointCount != GridPointCount {
		return 0, fmt.Errorf("expected run credits: grid point count = %d, want %d", pointCount, GridPointCount)
	}
	return queryCount * pointCount * MapsCreditsPerCall, nil
}

// BuildGeoGrid builds the fixed 3x3 grid of sampling points inscribed in a
// circle of radiusM around latitude/longitude. Each point is the true
// spherical destination of its planar east/north offset at bearing
// atan2(east, north), so corners sit at radiusM, edge midpoints at
// radiusM/sqrt(2), and the centre at zero. Rows run north to south and columns
// west to east, with row-major point indices. Latitude must be within
// [-90,90], longitude within [-180,180], and radiusM within [1000,25000]; the
// returned longitudes are normalised to [-180,180].
func BuildGeoGrid(latitude, longitude float64, radiusM int) ([]GridPoint, error) {
	if math.IsNaN(latitude) || math.IsInf(latitude, 0) {
		return nil, errors.New("build geo grid: latitude must be a finite number")
	}
	if math.IsNaN(longitude) || math.IsInf(longitude, 0) {
		return nil, errors.New("build geo grid: longitude must be a finite number")
	}
	if latitude < -90 || latitude > 90 {
		return nil, fmt.Errorf("build geo grid: latitude %v out of range [-90,90]", latitude)
	}
	if longitude < -180 || longitude > 180 {
		return nil, fmt.Errorf("build geo grid: longitude %v out of range [-180,180]", longitude)
	}
	if radiusM < 1000 || radiusM > 25000 {
		return nil, fmt.Errorf("build geo grid: radius_m %d out of range [1000,25000]", radiusM)
	}

	halfWidth := float64(radiusM) / math.Sqrt2
	latRad := latitude * math.Pi / 180
	lonRad := longitude * math.Pi / 180
	sinLat, cosLat := math.Sincos(latRad)

	sectors := [3][3]string{
		{"NW", "N", "NE"},
		{"W", "centre", "E"},
		{"SW", "S", "SE"},
	}

	points := make([]GridPoint, 0, GridPointCount)
	for row := 0; row < 3; row++ {
		north := float64(1-row) * halfWidth
		for col := 0; col < 3; col++ {
			east := float64(col-1) * halfWidth
			distance := math.Hypot(east, north)
			bearing := math.Atan2(east, north)

			angular := distance / earthRadiusM
			pointLat := math.Asin(sinLat*math.Cos(angular) + cosLat*math.Sin(angular)*math.Cos(bearing))
			pointLon := lonRad + math.Atan2(math.Sin(bearing)*math.Sin(angular)*cosLat, math.Cos(angular)-sinLat*math.Sin(pointLat))

			pointLon = math.Mod(pointLon*180/math.Pi+180, 360)
			if pointLon < 0 {
				pointLon += 360
			}
			pointLon -= 180

			ring := "edge"
			if row == 1 && col == 1 {
				ring = "centre"
			} else if row != 1 && col != 1 {
				ring = "corner"
			}

			points = append(points, GridPoint{
				PointIndex: row*3 + col,
				Latitude:   pointLat * 180 / math.Pi,
				Longitude:  pointLon,
				DistanceM:  distance,
				Ring:       ring,
				Sector:     sectors[row][col],
			})
		}
	}
	return points, nil
}
