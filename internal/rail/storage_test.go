package rail

import "github.com/1broseidon/ghostmux/internal/state"

func groupsPath() string { return state.DefaultPath() }

func loadState() ([]Group, map[string]bool, map[string]string) {
	store, _ := state.OpenDefault()
	groups, collapsed, dirs, _ := railState(store.Snapshot())
	return groups, collapsed, dirs
}

func saveState(groups []Group, collapsed map[string]bool, dirs map[string]string) error {
	store, err := state.OpenDefault()
	if err != nil {
		return err
	}
	return store.Update(func(doc *state.Document) error {
		setRailDocument(doc, groups, collapsed, dirs)
		return nil
	})
}

func LoadSettings() Settings {
	store, _ := state.OpenDefault()
	doc := store.Snapshot()
	if doc.Settings == nil {
		return Settings{}
	}
	return *doc.Settings
}

func SaveSettings(settings Settings) error {
	store, err := state.OpenDefault()
	if err != nil {
		return err
	}
	return store.Update(func(doc *state.Document) error {
		if settings.Empty() {
			doc.Settings = nil
		} else {
			copy := settings
			doc.Settings = &copy
		}
		return nil
	})
}

func StateFile() state.Info {
	store, _ := state.OpenDefault()
	return store.Info()
}
