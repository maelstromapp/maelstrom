package v1

type nameValueByName []NameValue

func (s nameValueByName) Len() int           { return len(s) }
func (s nameValueByName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
func (s nameValueByName) Less(i, j int) bool { return s[i].Name < s[j].Name }

type componentWithEventSourcesByName []ComponentWithEventSources

func (s componentWithEventSourcesByName) Len() int      { return len(s) }
func (s componentWithEventSourcesByName) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s componentWithEventSourcesByName) Less(i, j int) bool {
	return s[i].Component.Name < s[j].Component.Name
}
