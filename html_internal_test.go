package views

import (
	"io"
	"runtime"
	"sync"
	"testing"
)

// Renders that wait inside a template function each hold an instance of the
// set, so a burst of them makes many. Once they are back, the engine keeps no
// more than it needs for renders that only compute, and leaves the rest to
// the garbage collector.
func TestHTMLIdleInstancesAreCapped(t *testing.T) {
	renders := runtime.GOMAXPROCS(0) * 4

	started := make(chan struct{}, renders)
	release := make(chan struct{})

	templates := HTML(nil, ".html")
	templates.AddFunc("wait", func() string {
		started <- struct{}{}
		<-release
		return ""
	})
	if err := templates.ParseTemplate("page.html", []byte(`{{ wait }}done`), nil); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range renders {
		wg.Go(func() {
			if err := templates.ExecuteTemplate(io.Discard, "page.html", "", nil); err != nil {
				t.Error(err)
			}
		})
	}

	// Every render is inside the function now, each with an instance.
	for range renders {
		<-started
	}
	close(release)
	wg.Wait()

	set := templates.set.Load()

	set.mu.Lock()
	idle := len(set.free)
	set.mu.Unlock()

	if idle == 0 || idle > set.maxIdle {
		t.Fatalf("expected between 1 and %d idle instances after %d overlapping renders, got %d", set.maxIdle, renders, idle)
	}
}
