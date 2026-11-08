package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gtr-manager/internal/gomodules"
	"gtr-manager/internal/source"
)

func TestParallel(t *testing.T) {
	var running, peak int32
	keys := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	var mu sync.Mutex
	done := map[string]bool{}
	err := parallel(context.Background(), 3, keys, func(ctx context.Context, k string) error {
		n := atomic.AddInt32(&running, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		mu.Lock()
		done[k] = true
		mu.Unlock()
		return nil
	})
	if err != nil || len(done) != len(keys) || peak > 3 || peak < 2 {
		t.Fatalf("err %v, done %d, peak %d", err, len(done), peak)
	}

	// The failure that happened on its own wins over failures of calls that
	// were cancelled because of it (a killed git, a closed connection).
	bStarted := make(chan struct{})
	err = parallel(context.Background(), 8, keys, func(ctx context.Context, k string) error {
		switch k {
		case "b":
			close(bStarted)
			<-ctx.Done()
			return errors.New("b: killed")
		case "d":
			<-bStarted
			return errors.New("d failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
			return nil
		}
	})
	if err == nil || err.Error() != "d failed" {
		t.Fatalf("err = %v, want d's", err)
	}

	// After a failure no new work starts.
	var started int32
	err = parallel(context.Background(), 1, keys, func(ctx context.Context, k string) error {
		atomic.AddInt32(&started, 1)
		return fmt.Errorf("%s failed", k)
	})
	if err == nil || err.Error() != "a failed" || started != 1 {
		t.Fatalf("err %v, started %d", err, started)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := parallel(ctx, 2, keys, func(context.Context, string) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

// offlineProject installs online, then cuts every network source.
func offlineProject(t *testing.T) *project {
	t.Helper()
	p := inspected(t) // orm, orm-mysql (+ strutil), example.com/top (+ low)
	unreachable := "http://127.0.0.1:1"
	p.in.GitHub = &source.GitHub{API: unreachable, Tmp: p.home.Tmp()}
	p.in.GoProxy.Public.URL = unreachable
	p.in.SumDB = &gomodules.SumDB{URL: unreachable}
	p.in.Offline = true
	return p
}

func TestOfflineInstall(t *testing.T) {
	p := offlineProject(t)
	lockBefore := p.read("gtr.lock")
	os.RemoveAll(filepath.Join(p.dir, ModulesDir))
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatalf("offline install: %v\n%s", err, p.log)
	}
	if p.read("gtr.lock") != lockBefore {
		t.Fatalf("gtr.lock changed:\n%s\nwas:\n%s", p.read("gtr.lock"), lockBefore)
	}
	if _, err := os.Stat(filepath.Join(p.dir, ModulesDir, "orm", "orm.go")); err != nil {
		t.Fatal("orm not linked")
	}
	if err := p.in.Install(ctx, Options{Frozen: true}); err != nil {
		t.Fatalf("offline ci: %v", err)
	}

	// The module cache is gone: go.mod files come from the store.
	os.RemoveAll(filepath.Join(p.home.Root, "cache", "download", "go"))
	if err := p.in.Install(ctx, Options{Frozen: true}); err != nil {
		t.Fatalf("offline ci without the cache: %v", err)
	}
}

func TestOfflineNewProjectUsesStore(t *testing.T) {
	p := offlineProject(t)
	// Another project on the same machine, no gtr.lock: the stored
	// versions are enough, the newest matching one wins.
	q := &project{dir: t.TempDir(), home: p.home, log: p.log, gh: p.gh}
	os.WriteFile(filepath.Join(q.dir, "gtr.json"), []byte(`{"name":"other","version":"0.1.0","dependencies":{"orm-mysql":"github:shibisty/orm.go-mysql-driver#^0.1","orm":"github:shibisty/orm.go#*","example.com/top":"^1"}}`), 0o644)
	q.in = &Installer{Dir: q.dir, Home: p.home, GitHub: p.in.GitHub, GoProxy: p.in.GoProxy, SumDB: p.in.SumDB, Log: p.log, Offline: true}
	if err := q.in.Install(ctx, Options{}); err != nil {
		t.Fatalf("%v\n%s", err, p.log)
	}
	l := q.lock(t)
	if l.Packages["orm"].Version != "0.1.3" || l.Packages["strutil"].Version != "1.0.0" || l.Packages["example.com/low"].Version != "v1.1.0" {
		t.Fatalf("lock: %+v", l.Packages)
	}

	// Something never downloaded is an offline error, not a hang.
	os.WriteFile(filepath.Join(q.dir, "gtr.json"), []byte(`{"name":"other","version":"0.1.0","dependencies":{"faker":"github:shibisty/faker.go#^0.1"}}`), 0o644)
	err := q.in.Install(ctx, Options{})
	if !errors.Is(err, ErrOffline) || !strings.Contains(err.Error(), "faker is not in the store") {
		t.Fatalf("missing package: %v", err)
	}
	os.WriteFile(filepath.Join(q.dir, "gtr.json"), []byte(`{"name":"other","version":"0.1.0","dependencies":{"example.com/none":"^1"}}`), 0o644)
	if err := q.in.Install(ctx, Options{}); err == nil {
		t.Fatal("missing module: want an error")
	}
}

func TestOfflineAddBySource(t *testing.T) {
	p := offlineProject(t)
	if err := p.in.Remove(ctx, []string{"orm-mysql"}); err != nil {
		t.Fatalf("offline remove: %v", err)
	}
	// orm-mysql is in the store, but gtr.lock no longer says which source
	// it came from, so its name is unknown offline.
	p.in.offline = nil // a new run
	err := p.in.Add(ctx, []string{"github:shibisty/orm.go-mysql-driver"}, false)
	if !errors.Is(err, ErrOffline) || !strings.Contains(err.Error(), "name") {
		t.Fatalf("unknown name: %v", err)
	}
	// A source gtr.lock knows works.
	if err := p.in.Add(ctx, []string{"github:shibisty/orm.go#^0.1"}, false); err != nil {
		t.Fatalf("offline add of a locked source: %v", err)
	}
}

// A package of the same name and version from another source is never
// taken offline, even if it was stored later.
func TestOfflineKeepsSourcesApart(t *testing.T) {
	p := offlineProject(t)
	impostor := filepath.Join(p.home.Root, "store", "gtr", "orm@0.1.3-00000000")
	os.MkdirAll(impostor, 0o755)
	os.WriteFile(filepath.Join(impostor, "gtr.json"), []byte(gtrJSON("orm", nil, nil)), 0o644)
	os.WriteFile(filepath.Join(impostor, "orm.go"), []byte("package orm // impostor"), 0o644)
	os.WriteFile(impostor+".sources", []byte("github:mallory/orm\n"), 0o644)
	future := time.Now().Add(time.Hour)
	os.Chtimes(impostor, future, future)

	q := &project{dir: t.TempDir(), home: p.home, log: p.log, gh: p.gh}
	os.WriteFile(filepath.Join(q.dir, "gtr.json"), []byte(`{"name":"other","version":"0.1.0","dependencies":{"orm":"github:shibisty/orm.go#^0.1"}}`), 0o644)
	q.in = &Installer{Dir: q.dir, Home: p.home, GitHub: p.in.GitHub, GoProxy: p.in.GoProxy, Log: p.log, Offline: true}
	if err := q.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(q.read("gtr_modules/orm/orm.go"), "impostor") {
		t.Fatal("offline install took a package from another source")
	}
	os.WriteFile(filepath.Join(q.dir, "gtr.json"), []byte(`{"name":"other","version":"0.1.0","dependencies":{"orm":"github:mallory/orm#^0.1"}}`), 0o644)
	os.Remove(filepath.Join(q.dir, "gtr.lock"))
	q.in.offline = nil
	if err := q.in.Install(ctx, Options{}); err != nil || !strings.Contains(q.read("gtr_modules/orm/orm.go"), "impostor") {
		t.Fatalf("its own source: %v", err)
	}
}
