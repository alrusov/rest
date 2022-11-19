/*
Описание структур для API и инициализация
*/
package rest

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/alrusov/config"
	"github.com/alrusov/log"
	"github.com/alrusov/misc"
	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

func Init(cfg any, hh *stdhttp.HTTP, basePath string, defaultDB string, extraConfigs misc.InterfaceMap) (err error) {
	appCfg = cfg
	httpHdl = hh
	base = basePath
	defDB = defaultDB
	configs = extraConfigs

	Log.Message(log.INFO, "Initialized")
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func ModuleRegistration(handler API) (err error) {
	info := handler.Info()
	if info == nil {
		return fmt.Errorf(`info is nil for [%#v]"`, handler)
	}

	defer func() {
		if err != nil {
			err = fmt.Errorf("[%s] %s", info.Path, err)
			return
		}
	}()

	info.Path = strings.TrimSpace(info.Path)
	if info.Path != "/" {
		info.Path = misc.NormalizeSlashes(info.Path)
	}

	if info.Name == "" {
		info.Name = strings.ReplaceAll(info.Path, "/", ".")
	}

	url := info.Path
	relURL := url
	if url == "/" {
		url = ""
		relURL = "/"
	}

	if len(url) == 0 || url[0] != '/' {
		url = fmt.Sprintf("%s/%s", base, info.Path)
	}
	url = misc.NormalizeSlashes(url)

	err = loadEndpointConfig(relURL, info)
	if err != nil {
		return
	}

	err = info.Methods.Prepare()
	if err != nil {
		return
	}

	err = info.makeParamsDescription()
	if err != nil {
		return
	}

	if info.DBtype == "" {
		info.DBtype = defDB
	}

	if info.QueryPrefix != "" && !strings.HasSuffix(info.QueryPrefix, ".") {
		info.QueryPrefix += "."
	}

	p := &module{
		relativeURL: relURL,
		handler:     handler,
		info:        info,
		logFacility: log.NewFacility(url),
	}

	modulesMutex.Lock()
	modules[url] = p
	modulesMutex.Unlock()

	httpHdl.AddEndpointsInfo(
		misc.StringMap{
			url: info.Summary,
		},
	)

	if info.Init != nil {
		err = info.Init(info)
		if err != nil {
			return
		}
	}

	p.logFacility.Message(log.INFO, "Initialized")

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func loadEndpointConfig(relURL string, info *Info) (err error) {
	urlCfg, exists := configs[relURL]
	if !exists {
		urlCfg = map[string]any{}
	}

	if info.Config == nil {
		return fmt.Errorf(`info.Config is nil`)
	}

	if reflect.ValueOf(info.Config).Kind() != reflect.Ptr {
		return fmt.Errorf(`info.Config is not a pointer`)
	}

	if reflect.Indirect(reflect.ValueOf(info.Config)).Kind() != reflect.Struct {
		return fmt.Errorf(`info.Config is pointer to %s, expected pointer to %s`, reflect.Indirect(reflect.ValueOf(info.Config)).Kind().String(), reflect.Struct.String())
	}

	err = config.ConvExtra(&urlCfg, info.Config)
	if err != nil {
		return fmt.Errorf("%s", err)
	}

	configs[relURL] = urlCfg

	m := reflect.ValueOf(info.Config).MethodByName("Check")

	if m.Kind() != reflect.Func {
		return fmt.Errorf(`%T doesn't have the Check function`, info.Config)
	}

	e := m.Call([]reflect.Value{reflect.ValueOf(appCfg)})
	if e[0].IsNil() {
		return
	}

	err, ok := e[0].Interface().(error)
	if !ok {
		return fmt.Errorf(`config.Check: returned not error type value (%T)`, e[0].Interface())
	}

	if err != nil {
		return fmt.Errorf(`config.Check: %s`, err)
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func Start() (err error) {
	err = lookingForUnusedConfigs()
	if err != nil {
		return
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func lookingForUnusedConfigs() (err error) {
	msgs := misc.NewMessages()

	notProcessedTp := reflect.ValueOf(map[string]any{}).Type()

	for name, c := range configs {
		v := reflect.ValueOf(c)
		if notProcessedTp == v.Type() {
			msgs.Add(`api.configs contains data for unknown endpoint "%s" (%v)`, name, v)
		}
	}

	return msgs.Error()
}

//----------------------------------------------------------------------------------------------------------------------------//
