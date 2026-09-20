package onboarding

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Device registry and invitation readers run in the service while the desktop
// changes them. Readers must always see one complete generation of the store.
func TestJSONStoreConcurrentReadersNeverSeePartialReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	value := map[string]any{"nodes": []map[string]string{{"node_id": "A", "display_name": strings.Repeat("设备", 1024)}}}
	if err := writePrettyJSON(path, value); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	failures := make(chan error, 8)
	var readers sync.WaitGroup
	for i := 0; i < 4; i++ {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				data, err := readJSONFile(path)
				if err != nil {
					select {
					case failures <- err:
					default:
					}
					return
				}
				var store map[string]any
				if err := json.Unmarshal(data, &store); err != nil {
					select {
					case failures <- err:
					default:
					}
					return
				}
			}
		})
	}
	for i := 0; i < 100; i++ {
		if err := writePrettyJSON(path, value); err != nil {
			close(stop)
			readers.Wait()
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
}
