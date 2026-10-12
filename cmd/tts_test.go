package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spotify/save-to-spotify/config"
)

// The upstream kokoro-onnx repo mixes library releases, language-specific
// model releases (v1.1-zh), and quantized variants (fp16, int8) — the
// resolver must skip all of those and land on the newest release carrying
// the canonical full-precision files.
func TestResolveKokoroModelAssets(t *testing.T) {
	const releasesJSON = `[
		{"tag_name": "v0.4.9", "assets": [{"name": "kokoro_onnx-0.4.9.tar.gz", "browser_download_url": "https://example.com/lib.tar.gz"}]},
		{"tag_name": "model-files-v1.1", "assets": [
			{"name": "kokoro-v1.1-zh.onnx", "browser_download_url": "https://example.com/kokoro-v1.1-zh.onnx"},
			{"name": "voices-v1.1-zh.bin", "browser_download_url": "https://example.com/voices-v1.1-zh.bin"}
		]},
		{"tag_name": "model-files-v1.0", "assets": [
			{"name": "kokoro-v1.0.fp16-gpu.onnx", "browser_download_url": "https://example.com/kokoro-v1.0.fp16-gpu.onnx"},
			{"name": "kokoro-v1.0.fp16.onnx", "browser_download_url": "https://example.com/kokoro-v1.0.fp16.onnx"},
			{"name": "kokoro-v1.0.int8.onnx", "browser_download_url": "https://example.com/kokoro-v1.0.int8.onnx"},
			{"name": "kokoro-v1.0.onnx", "browser_download_url": "https://example.com/kokoro-v1.0.onnx"},
			{"name": "voices-v1.0.bin", "browser_download_url": "https://example.com/voices-v1.0.bin"}
		]}
	]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(releasesJSON))
	}))
	defer srv.Close()
	t.Setenv("SAVE_TO_SPOTIFY_KOKORO_RELEASES_API_URL", srv.URL)

	assets, err := resolveKokoroModelAssets()
	if err != nil {
		t.Fatalf("resolveKokoroModelAssets: %v", err)
	}

	if got := assets["kokoro-v*.onnx"]; got.Name != "kokoro-v1.0.onnx" || !strings.HasSuffix(got.URL, "/kokoro-v1.0.onnx") {
		t.Errorf("onnx asset = %+v, want the canonical kokoro-v1.0.onnx (not zh or quantized variants)", got)
	}
	if got := assets["voices-v*.bin"]; got.Name != "voices-v1.0.bin" || !strings.HasSuffix(got.URL, "/voices-v1.0.bin") {
		t.Errorf("voices asset = %+v, want voices-v1.0.bin", got)
	}
}

func TestResolveKokoroModelAssetsFutureRelease(t *testing.T) {
	// A future standard-named release must win over older ones.
	const releasesJSON = `[
		{"tag_name": "model-files-v1.2", "assets": [
			{"name": "kokoro-v1.2.onnx", "browser_download_url": "https://example.com/kokoro-v1.2.onnx"},
			{"name": "voices-v1.2.bin", "browser_download_url": "https://example.com/voices-v1.2.bin"}
		]},
		{"tag_name": "model-files-v1.0", "assets": [
			{"name": "kokoro-v1.0.onnx", "browser_download_url": "https://example.com/kokoro-v1.0.onnx"},
			{"name": "voices-v1.0.bin", "browser_download_url": "https://example.com/voices-v1.0.bin"}
		]}
	]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(releasesJSON))
	}))
	defer srv.Close()
	t.Setenv("SAVE_TO_SPOTIFY_KOKORO_RELEASES_API_URL", srv.URL)

	assets, err := resolveKokoroModelAssets()
	if err != nil {
		t.Fatalf("resolveKokoroModelAssets: %v", err)
	}
	if got := assets["kokoro-v*.onnx"]; got.Name != "kokoro-v1.2.onnx" || !strings.HasSuffix(got.URL, "/kokoro-v1.2.onnx") {
		t.Errorf("onnx asset = %+v, want the newest kokoro-v1.2.onnx", got)
	}
}

func TestDownloadKokoroModelsUsesAssetNames(t *testing.T) {
	for _, source := range []string{"releases", "fallback"} {
		t.Run(source, func(t *testing.T) {
			for _, existingConfig := range []bool{false, true} {
				t.Run(fmt.Sprintf("existing_config=%t", existingConfig), func(t *testing.T) {
					t.Setenv("XDG_CONFIG_HOME", t.TempDir())
					configDir, err := config.ConfigDir()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(configDir, 0700); err != nil {
						t.Fatal(err)
					}
					configPath := filepath.Join(configDir, "tts.json")
					const originalConfig = `{"default":"openai","engines":[]}`
					if existingConfig {
						if err := os.WriteFile(configPath, []byte(originalConfig), 0600); err != nil {
							t.Fatal(err)
						}
					}

					const modelBody = "model fixture"
					var srv *httptest.Server
					srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/releases":
							if source == "fallback" {
								http.Error(w, "API unavailable", http.StatusServiceUnavailable)
								return
							}
							fmt.Fprintf(w, `[{"tag_name":"model-files-v9.9","assets":[
						{"name":"kokoro-v9.9.onnx","browser_download_url":%q},
						{"name":"voices-v9.9.bin","browser_download_url":%q}
					]}]`, srv.URL+"/tts.json", srv.URL+"/opaque-download?signature=123")
						case "/tts.json", "/opaque-download":
							w.Write([]byte(modelBody))
						default:
							http.NotFound(w, r)
						}
					}))
					defer srv.Close()
					t.Setenv("SAVE_TO_SPOTIFY_KOKORO_RELEASES_API_URL", srv.URL+"/releases")
					if source == "fallback" {
						original := kokoroFallbackAssets
						t.Cleanup(func() { kokoroFallbackAssets = original })
						kokoroFallbackAssets = map[string]kokoroAsset{
							"kokoro-v*.onnx": {Name: "kokoro-v9.9.onnx", URL: srv.URL + "/tts.json"},
							"voices-v*.bin":  {Name: "voices-v9.9.bin", URL: srv.URL + "/opaque-download?signature=123"},
						}
					}

					if err := downloadKokoroModels(configDir); err != nil {
						t.Fatal(err)
					}
					data, err := os.ReadFile(configPath)
					if existingConfig {
						if err != nil || string(data) != originalConfig {
							t.Errorf("TTS config changed: got %q, err %v", data, err)
						}
					} else if !os.IsNotExist(err) {
						t.Errorf("unexpected TTS config created: got %q, err %v", data, err)
					}
					for _, name := range []string{"kokoro-v9.9.onnx", "voices-v9.9.bin"} {
						data, err := os.ReadFile(filepath.Join(configDir, name))
						if err != nil || string(data) != modelBody {
							t.Errorf("model %s = %q, err %v", name, data, err)
						}
					}
					entries, err := os.ReadDir(configDir)
					if err != nil {
						t.Fatal(err)
					}
					wantFiles := 2
					if existingConfig {
						wantFiles++
					}
					if len(entries) != wantFiles {
						t.Errorf("config directory contains %d entries, want %d: %v", len(entries), wantFiles, entries)
					}
				})
			}
		})
	}
}

func TestDownloadKokoroModelsRejectsInvalidFallbackName(t *testing.T) {
	for _, name := range []string{"tts.json", "../tts.json", `..\tts.json`, "voices-v1.0.bin", ""} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			configDir, err := config.ConfigDir()
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/releases" {
					t.Errorf("invalid asset was requested: %s", r.URL.Path)
				}
				http.Error(w, "API unavailable", http.StatusServiceUnavailable)
			}))
			defer srv.Close()
			t.Setenv("SAVE_TO_SPOTIFY_KOKORO_RELEASES_API_URL", srv.URL+"/releases")
			original := kokoroFallbackAssets
			t.Cleanup(func() { kokoroFallbackAssets = original })
			kokoroFallbackAssets = map[string]kokoroAsset{
				"kokoro-v*.onnx": {Name: name, URL: srv.URL + "/model"},
			}

			if err := downloadKokoroModels(configDir); err == nil || !strings.Contains(err.Error(), "invalid asset name") {
				t.Fatalf("want invalid asset name error, got %v", err)
			}
		})
	}
}

func TestRunOpenAITest(t *testing.T) {
	audio := []byte("RIFF-fake-wav-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth header = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["response_format"] != "wav" || body["voice"] != "alloy" {
			t.Errorf("unexpected payload: %v", body)
		}
		w.Write(audio)
	}))
	defer srv.Close()
	// SDK semantics: OPENAI_BASE_URL is the complete base INCLUDING /v1 —
	// the handler asserting /v1/audio/speech catches /v1 duplication.
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("OPENAI_API_KEY", "test-key")

	out := filepath.Join(t.TempDir(), "preview.wav")
	if err := runOpenAITest("hello", "", out); err != nil {
		t.Fatalf("runOpenAITest: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != string(audio) {
		t.Fatalf("written file = %q, err %v", got, err)
	}
}

func TestRunElevenLabsTest(t *testing.T) {
	audio := []byte("fake-mp3-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("xi-api-key"); got != "el-key" {
			t.Errorf("xi-api-key = %q", got)
		}
		switch r.URL.Path {
		case "/v1/voices":
			w.Write([]byte(`{"voices":[{"voice_id":"abc123","name":"Rachel"}]}`))
		case "/v1/text-to-speech/abc123":
			w.Write(audio)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	t.Setenv("ELEVENLABS_BASE_URL", srv.URL)
	t.Setenv("ELEVENLABS_API_KEY", "el-key")

	out := filepath.Join(t.TempDir(), "preview.mp3")
	if err := runElevenLabsTest("hello", "", out); err != nil {
		t.Fatalf("runElevenLabsTest: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != string(audio) {
		t.Fatalf("written file = %q, err %v", got, err)
	}

	// Unknown voice surfaces a clear error instead of a synthesis attempt.
	if err := runElevenLabsTest("hello", "Nobody", out); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected voice-not-found error, got %v", err)
	}
}
