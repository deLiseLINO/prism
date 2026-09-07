package tui

func (m *Model) pruneAccountKeyedMaps() {
	if len(m.LoadingMap) == 0 && len(m.ErrorsMap) == 0 && len(m.silentRefresh) == 0 {
		return
	}
	valid := make(map[string]struct{}, len(m.Accounts))
	for i := range m.Accounts {
		if m.Accounts[i].ID != "" {
			valid[m.Accounts[i].ID] = struct{}{}
		}
	}
	for key := range m.LoadingMap {
		if _, ok := valid[key]; !ok {
			delete(m.LoadingMap, key)
		}
	}
	for key := range m.ErrorsMap {
		if _, ok := valid[key]; !ok {
			delete(m.ErrorsMap, key)
		}
	}
	for key := range m.silentRefresh {
		if _, ok := valid[key]; !ok {
			delete(m.silentRefresh, key)
		}
	}
}
