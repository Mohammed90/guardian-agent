package policy

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

type Scope struct {
	ClientUsername  string `json:"ClientUsername"`
	ClientHostname  string `json:"ClientHostname"`
	ClientPort      uint32 `json:"ClientPort"`
	ServiceUsername string `json:"ServiceUsername"`
	ServiceHostname string `json:"ServiceHostname"`
}

type Rule struct {
	AllCommands bool     `json:"AllCommands"`
	Commands    []string `json:"Commands"`
}

type Store struct {
	rules map[Scope]Rule
	path  string
}

var mutex sync.RWMutex

type storageEntry struct {
	PolicyScope Scope `json:"Scope"`
	PolicyRule  Rule  `json:"Rule"`
}

func NewStore(configPath string) (err error, store Store) {
	store = Store{
		path: configPath,
	}
	err = store.load()
	mutex = sync.RWMutex{}

	return err, store
}

func (rule Rule) IsApproved(reqCommand string) bool {
	if rule.AllCommands {
		return true
	}
	for _, storedCommand := range rule.Commands {
		if reqCommand == storedCommand {
			return true
		}
	}
	return false
}

func (store Store) GetRule(scope Scope) Rule {
	mutex.RLock()
	storedRule, ok := store.rules[scope]
	mutex.RUnlock()
	if ok {
		return storedRule
	} else {
		return Rule{AllCommands: false, Commands: make([]string, 0)}
	}
}

func (store Store) SetAllAllowedInScope(sc Scope) (err error) {
	rule := store.GetRule(sc)
	rule.AllCommands = true
	mutex.Lock()
	store.rules[sc] = rule
	mutex.Unlock()
	err = store.save()

	return
}

func (store Store) SetCommandAllowedInScope(sc Scope, newCommand string) (err error) {
	rule := store.GetRule(sc)
	for _, command := range rule.Commands {
		if command == newCommand {
			return
		}
	}
	rule.Commands = append(rule.Commands, newCommand)
	mutex.Lock()
	store.rules[sc] = rule
	mutex.Unlock()
	err = store.save()

	return
}

func (store *Store) load() (err error) {
	file, err := os.OpenFile(store.path, os.O_RDONLY|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer file.Close()

	dec := json.NewDecoder(file)
	if dec.More() {
		if err = dec.Decode(&store.rules); err != nil {
			log.Printf("err is %s", err)
			return err
		}
	}
	return nil
}

func (store *Store) save() (err error) {
	file, err := os.OpenFile(store.path, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer file.Close()

	enc := json.NewEncoder(file)
	mutex.Lock()
	defer mutex.Unlock()
	if err := enc.Encode(&store); err != nil {
		return err
	}
	return nil
}

func (store *Store) MarshalJSON() ([]byte, error) {
	ps := []storageEntry{}
	for k, v := range store.rules {
		ps = append(ps, storageEntry{PolicyScope: k, PolicyRule: v})
	}
	val, err := json.Marshal(ps)

	if err != nil {
		return nil, err
	}
	log.Printf("Weird bug2 %s", string(val))

	return val, nil
}

func (store Store) UnmarshalJSON(b []byte) error {
	tmpStore := []storageEntry{}
	err := json.Unmarshal(b, &tmpStore)

	if err != nil {
		return err
	}
	for _, v := range tmpStore {
		store.rules[v.PolicyScope] = v.PolicyRule
	}

	return nil
}
