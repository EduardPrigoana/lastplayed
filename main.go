package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	lastFmBaseURL  = "https://ws.audioscrobbler.com/2.0/"
	defaultTimeout = 10 * time.Second
)

type lastFmResponse struct {
	RecentTracks recentTracks `json:"recenttracks"`
}

type recentTracks struct {
	Track []track `json:"track"`
}

type track struct {
	Artist     artist  `json:"artist"`
	Album      album   `json:"album"`
	Image      []image `json:"image"`
	Streamable string  `json:"streamable"`
	Date       *date   `json:"date,omitempty"`
	URL        string  `json:"url"`
	Name       string  `json:"name"`
	MBID       string  `json:"mbid"`
	NowPlaying attr    `json:"@attr"`
}

type artist struct {
	Text string `json:"#text"`
	MBID string `json:"mbid"`
}

type album struct {
	Text string `json:"#text"`
	MBID string `json:"mbid"`
}

type image struct {
	Text string `json:"#text"`
	Size string `json:"size"`
}

type date struct {
	UTS string `json:"uts"`
}

type attr struct {
	NowPlaying string `json:"nowplaying"`
}

// trackWithDateUTS is used for the final JSON output.
// It embeds the original track struct but adds a numeric DateUTS field
// and is intended to replace the original object-based Date field.
type trackWithDateUTS struct {
	track
	DateUTS *int64 `json:"date_uts,omitempty"`
}

type shieldsResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	Label         string `json:"label"`
	Message       string `json:"message"`
}

type cacheEntry struct {
	track     *track
	lastFetch time.Time
}

type TrackCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
}

func NewTrackCache() *TrackCache {
	c := &TrackCache{
		entries: make(map[string]*cacheEntry),
	}
	c.startCleanupRoutine()
	return c
}

func (c *TrackCache) startCleanupRoutine() {
	ticker := time.NewTicker(1 * time.Hour)
	go func() {
		for range ticker.C {
			c.mu.Lock()
			for key, entry := range c.entries {
				if time.Since(entry.lastFetch) > 24*time.Hour {
					delete(c.entries, key)
				}
			}
			c.mu.Unlock()
		}
	}()
}

type Server struct {
	apiKey     string
	httpClient *http.Client
	cache      *TrackCache
}

func NewServer(apiKey string) (*Server, error) {
	if apiKey == "" {
		return nil, errors.New("LASTFM_API_KEY is not set")
	}
	return &Server{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
		cache: NewTrackCache(),
	}, nil
}

func (s *Server) serveTrackAndRefresh(ctx context.Context, user string) (*track, error) {
	s.cache.mu.RLock()
	entry, found := s.cache.entries[user]
	s.cache.mu.RUnlock()

	if !found {
		newTrack, err := s.fetchLatestTrackFromAPI(ctx, user)
		if err != nil {
			return nil, err
		}

		s.cache.mu.Lock()
		s.cache.entries[user] = &cacheEntry{
			track:     newTrack,
			lastFetch: time.Now(),
		}
		s.cache.mu.Unlock()
		return newTrack, nil
	}

	s.cache.mu.Lock()
	if time.Since(entry.lastFetch) > time.Second {
		entry.lastFetch = time.Now()
		go s.updateCacheForUser(user)
	}
	s.cache.mu.Unlock()

	return entry.track, nil
}

func (s *Server) updateCacheForUser(user string) {
	newTrack, err := s.fetchLatestTrackFromAPI(context.Background(), user)
	if err != nil {
		// In case of an error, we keep the old data and log the error.
		fmt.Fprintf(os.Stderr, "WARN: Failed to update cache for user %s: %v\n", user, err)
		return
	}

	s.cache.mu.Lock()
	defer s.cache.mu.Unlock()

	if entry, found := s.cache.entries[user]; found {
		entry.track = newTrack
	}
}

func (s *Server) fetchLatestTrackFromAPI(ctx context.Context, user string) (*track, error) {
	baseURL, _ := url.Parse(lastFmBaseURL)
	params := url.Values{}
	params.Add("method", "user.getrecenttracks")
	params.Add("limit", "1")
	params.Add("format", "json")
	params.Add("user", user)
	params.Add("api_key", s.apiKey)
	baseURL.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("could not create API request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("last.fm API is unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("received non-200 status code (%d) from Last.fm API", resp.StatusCode)
	}

	var lastFmData lastFmResponse
	if err := json.NewDecoder(resp.Body).Decode(&lastFmData); err != nil {
		return nil, fmt.Errorf("could not parse Last.fm API response: %w", err)
	}

	if len(lastFmData.RecentTracks.Track) == 0 {
		return nil, nil
	}

	return &lastFmData.RecentTracks.Track[0], nil
}

func (s *Server) latestSongHandler(w http.ResponseWriter, r *http.Request) {
	user := strings.Trim(r.URL.Path, "/")
	if user == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "BAD_REQUEST"})
		return
	}

	sourceTrack, err := s.serveTrackAndRefresh(r.Context(), user)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"message": "UPSTREAM_ERROR"})
		return
	}

	if sourceTrack == nil {
		writeJSON(w, http.StatusOK, map[string]string{"message": "NO_TRACKS_FOUND"})
		return
	}

	if r.URL.Query().Get("format") == "shields.io" {
		shieldData := shieldsResponse{
			SchemaVersion: 1,
			Label:         "Last.fm",
			Message:       fmt.Sprintf("%s – %s", sourceTrack.Artist.Text, sourceTrack.Name),
		}
		if sourceTrack.NowPlaying.NowPlaying == "true" {
			shieldData.Label = "Now Playing"
		}
		writeJSON(w, http.StatusOK, shieldData)
		return
	}

	outputTrack := trackWithDateUTS{
		track: *sourceTrack,
	}

	if sourceTrack.Date != nil && sourceTrack.Date.UTS != "" {
		if uts, err := strconv.ParseInt(sourceTrack.Date.UTS, 10, 64); err == nil {
			outputTrack.DateUTS = &uts
		}
	}

	// Remove the original 'date' object to avoid redundancy in the JSON output.
	// The 'date' field in the 'track' struct has 'omitempty', so setting it to nil
	// will cause it to be excluded from the final JSON.
	outputTrack.Date = nil

	writeJSON(w, http.StatusOK, map[string]trackWithDateUTS{"track": outputTrack})
}

func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	server, err := NewServer(os.Getenv("LASTFM_API_KEY"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Failed to create server: %v\n", err)
		os.Exit(1)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", server.latestSongHandler)

	httpServer := &http.Server{
		Addr:         ":" + port,
		Handler:      corsMiddleware(mux),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	fmt.Printf("Server listening on port %s\n", port)

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "FATAL: Could not start server: %v\n", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fmt.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: Server forced to shutdown: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Server exiting.")
}
