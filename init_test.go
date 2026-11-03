package rest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alrusov/misc"
	path "github.com/alrusov/rest/v4/path"
	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

type (
	testConfig struct{}

	testModule struct {
		testAPI
		info *Info
	}
)

func (c *testConfig) Check(cfg any) (err error) { return }

func (m *testModule) Info() *Info { return m.info }

func newTestModule(urlPath string, init FuncInit) *testModule {
	return &testModule{
		info: &Info{
			Path:   urlPath,
			Config: &testConfig{},
			Methods: &path.Set{
				Methods: path.Methods{
					stdhttp.MethodGET: &path.Chains{
						Chains: path.ChainsList{
							{Tokens: []*path.Token{{VarName: path.VarIgnore}}},
						},
					},
				},
			},
			Init: init,
		},
	}
}

func useTestModules(t *testing.T) {
	modulesMutex.Lock()
	oldModules := modules
	modules = map[string]*Module{}
	modulesMutex.Unlock()

	configsMutex.Lock()
	oldConfigs := configs
	configs = misc.InterfaceMap{}
	configsMutex.Unlock()

	oldAppCfg := appCfg
	appCfg = &struct{}{}

	t.Cleanup(func() {
		appCfg = oldAppCfg
		modulesMutex.Lock()
		modules = oldModules
		modulesMutex.Unlock()

		configsMutex.Lock()
		configs = oldConfigs
		configsMutex.Unlock()
	})
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestModuleRegistrationInitUsesModules(t *testing.T) {
	useTestModules(t)

	sub := newTestModule("/test/sub", nil)

	var enumerated []string
	main := newTestModule("/test/main",
		func(info *Info) (err error) {
			// Регистрация вложенного модуля и перечисление -- раньше deadlock
			err = ModuleRegistration(sub)
			if err != nil {
				return
			}

			return Enumerate(func(urlPath string, info *Info) error {
				enumerated = append(enumerated, urlPath)
				return nil
			})
		},
	)

	done := make(chan error)
	go func() {
		done <- ModuleRegistration(main)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock")
	}

	if len(enumerated) != 1 || enumerated[0] != "/test/sub" {
		t.Errorf("got enumerated %q, expected [/test/sub]", enumerated)
	}

	for _, u := range []string{"/test/main", "/test/sub"} {
		if !isRegistered(u) {
			t.Errorf("%s is not registered", u)
		}
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestModuleRegistrationInitError(t *testing.T) {
	useTestModules(t)

	m := newTestModule("/test/failed",
		func(info *Info) error {
			return fmt.Errorf("init error")
		},
	)

	err := ModuleRegistration(m)
	if err == nil || !strings.Contains(err.Error(), "init error") {
		t.Fatalf(`got error "%v", expected "init error"`, err)
	}

	if isRegistered("/test/failed") {
		t.Error("module registered after Init error")
	}

	if _, _, _, found := findModule("/test/failed"); found {
		t.Error("requests are routed to the failed module")
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestModuleRegistrationDuplicate(t *testing.T) {
	useTestModules(t)

	err := ModuleRegistration(newTestModule("/test/dup", nil))
	if err != nil {
		t.Fatal(err)
	}

	initCalled := false
	err = ModuleRegistration(newTestModule("/test/dup",
		func(info *Info) error {
			initCalled = true
			return nil
		},
	))
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Errorf(`got error "%v", expected "already registered"`, err)
	}

	if initCalled {
		t.Error("Init called for the duplicate")
	}
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestModuleRegistrationNilConfigs(t *testing.T) {
	useTestModules(t)

	configsMutex.Lock()
	configs = nil
	configsMutex.Unlock()

	err := ModuleRegistration(newTestModule("/test/nilcfg", nil))
	if err != nil {
		t.Fatal(err)
	}

	if !isRegistered("/test/nilcfg") {
		t.Error("module is not registered")
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
