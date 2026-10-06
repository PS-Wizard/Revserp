package locationlandmarks

import (
	"fmt"
	"math"
)

const earthRadiusM = 6371000.0

// straightLineMeters returns the great-circle distance between two finite coordinates.
func straightLineMeters(latitudeA, longitudeA, latitudeB, longitudeB float64) float64 {
	latARad := latitudeA * math.Pi / 180
	latBRad := latitudeB * math.Pi / 180
	deltaLat := (latitudeB - latitudeA) * math.Pi / 180
	deltaLon := (longitudeB - longitudeA) * math.Pi / 180

	haversine := math.Sin(deltaLat/2)*math.Sin(deltaLat/2) +
		math.Cos(latARad)*math.Cos(latBRad)*math.Sin(deltaLon/2)*math.Sin(deltaLon/2)
	return 2 * earthRadiusM * math.Asin(math.Min(1, math.Sqrt(haversine)))
}

func validateLandmarkCoordinates(latitude, longitude float64) error {
	if math.IsNaN(latitude) || math.IsInf(latitude, 0) || latitude < -90 || latitude > 90 {
		return fmt.Errorf("latitude %v outside [-90,90]", latitude)
	}
	if math.IsNaN(longitude) || math.IsInf(longitude, 0) || longitude < -180 || longitude > 180 {
		return fmt.Errorf("longitude %v outside [-180,180]", longitude)
	}
	return nil
}
