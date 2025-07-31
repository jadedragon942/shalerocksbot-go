package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

/*********************************************************************
 * 3) Weather Functions (OpenWeatherMap)
 *********************************************************************/
var httpClient = &http.Client{Timeout: 10 * time.Second}

// For parsing Nominatim's JSON response
type nominatimResponse []struct {
	Lat string `json:"lat"`
	Lon string `json:"lon"`
	// You can add more fields if needed (e.g., display_name)
}

func geocodeViaNominatim(query string) (float64, float64, error) {
	baseURL := "https://nominatim.openstreetmap.org/search"
	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")

	reqURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return 0, 0, err
	}
	// IMPORTANT: set a custom User-Agent per Nominatim policy
	req.Header.Set("User-Agent", "shalerocksbot-go/"+version+" (djade942@gmail.com)")

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return 0, 0, fmt.Errorf("nominatim error status %d: %s",
			resp.StatusCode, string(bodyBytes))
	}

	var results nominatimResponse
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return 0, 0, err
	}

	if len(results) == 0 {
		return 0, 0, fmt.Errorf("no geocoding results for %q", query)
	}

	lat, err := strconv.ParseFloat(results[0].Lat, 64)
	if err != nil {
		return 0, 0, err
	}
	lon, err := strconv.ParseFloat(results[0].Lon, 64)
	if err != nil {
		return 0, 0, err
	}

	return lat, lon, nil
}

func fetchWeatherSummary25(location string) (string, error) {
	apiKey := os.Getenv("OWM_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("no OWM_API_KEY found in environment")
	}

	query := url.QueryEscape(location)
	apiURL := fmt.Sprintf("https://api.openweathermap.org/data/2.5/weather?q=%s&units=imperial&appid=%s",
		query, apiKey,
	)

	resp, err := httpClient.Get(apiURL)
	if err != nil {
		return "", fmt.Errorf("failed to get weather data: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var data struct {
		Name    string `json:"name"`
		Weather []struct {
			Description string `json:"description"`
		} `json:"weather"`
		Main struct {
			Temp float64 `json:"temp"`
		} `json:"main"`
		Sys struct {
			Country string `json:"country"`
		} `json:"sys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("failed to decode JSON: %v", err)
	}

	if data.Name == "" && len(data.Weather) == 0 {
		return "", fmt.Errorf("no weather info found for '%s'", location)
	}

	desc := "unknown"
	if len(data.Weather) > 0 {
		desc = data.Weather[0].Description
	}
	return fmt.Sprintf("It's %.1f°F with %s in %s, %s.",
		data.Main.Temp, desc, data.Name, data.Sys.Country), nil
}

func fetchWeatherSummary3(location string) (string, error) {
	// 1) Get your API key from env
	apiKey := os.Getenv("OWM_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("no OWM_API_KEY found in environment")
	}

	// 2) Geocode the user-provided location via Nominatim
	lat, lon, err := geocodeViaNominatim(location)
	if err != nil {
		return "", fmt.Errorf("failed to geocode %q: %v", location, err)
	}

	// 3) Call OpenWeatherMap One Call 3.0 API
	oneCallURL := fmt.Sprintf(
		"https://api.openweathermap.org/data/3.0/onecall?lat=%f&lon=%f&exclude=minutely,hourly,daily,alerts&units=imperial&appid=%s",
		lat, lon, apiKey,
	)

	resp, err := httpClient.Get(oneCallURL)
	if err != nil {
		return "", fmt.Errorf("failed to get weather data: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("OWM error status %d: %s",
			resp.StatusCode, string(bodyBytes))
	}

	// 4) Parse OWM One Call response
	var owmData struct {
		Lat     float64 `json:"lat"`
		Lon     float64 `json:"lon"`
		Current struct {
			Temp    float64 `json:"temp"`
			Weather []struct {
				Description string `json:"description"`
			} `json:"weather"`
		} `json:"current"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&owmData); err != nil {
		return "", fmt.Errorf("failed to decode OWM JSON: %v", err)
	}

	if len(owmData.Current.Weather) == 0 {
		return "", fmt.Errorf("no weather data in OWM response for %q", location)
	}

	// 5) Build a summary
	desc := owmData.Current.Weather[0].Description
	tempF := owmData.Current.Temp
	summary := fmt.Sprintf("It's %.1f°F with %s in %s (%.4f, %.4f).",
		tempF, desc, location, owmData.Lat, owmData.Lon)

	return summary, nil
}
