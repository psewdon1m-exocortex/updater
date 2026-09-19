package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"updater/internal/console"
)

func wyvernLines(view console.WyvernView) []string {
	s := view.Status
	lines := []string{"WYVERN / ADAPTERS AND CLIENTS", "Instance: " + console.Text(s.InstanceID), "State: " + console.Text(s.State), fmt.Sprintf("Configuration loaded: %t | accepting requests: %t", s.ConfigurationLoaded, s.Ready), fmt.Sprintf("Active requests: %d", s.ActiveRequests)}
	if !s.ConfigurationLoaded {
		lines = append(lines, "Connect Kernel and publish the first Adapter before using LLM functions.")
	}
	if s.ConfigError != "" {
		lines = append(lines, "Configuration: "+console.Text(s.ConfigError))
	}
	lines = append(lines, "", "ADAPTERS")
	if len(view.Catalog.Adapters) == 0 {
		lines = append(lines, "No Adapters configured.")
	}
	for _, a := range view.Catalog.Adapters {
		state := "enabled"
		if !a.Enabled {
			state = "disabled"
		}
		lines = append(lines, console.Text(a.Name)+" ["+console.Text(a.ID)+"] / "+console.Text(a.Driver)+" / "+state)
		for _, p := range a.Profiles {
			lines = append(lines, console.Text(p.Name)+": "+console.Text(p.Model)+" | "+console.Text(strings.Join(p.Capabilities, ", ")))
		}
	}
	lines = append(lines, "", "CLIENT BINDINGS")
	if len(view.Catalog.Clients) == 0 {
		lines = append(lines, "No clients linked.")
	}
	for _, c := range view.Catalog.Clients {
		state := "linked"
		if !c.Enabled {
			state = "revoked"
		}
		lines = append(lines, console.Text(c.ID)+" / "+state)
		if len(c.Bindings) == 0 {
			lines = append(lines, "Adapter not selected; LLM functions are not ready.")
		}
		functions := make([]string, 0, len(c.Bindings))
		for function := range c.Bindings {
			functions = append(functions, function)
		}
		sort.Strings(functions)
		for _, function := range functions {
			b := c.Bindings[function]
			ready := false
			for _, a := range view.Catalog.Adapters {
				if a.ID == b.AdapterID && a.Enabled {
					for _, p := range a.Profiles {
						if p.Name == b.Profile {
							ready = true
						}
					}
				}
			}
			lines = append(lines, fmt.Sprintf("%s -> %s / %s | ready: %t", console.Text(function), console.Text(b.AdapterID), console.Text(b.Profile), ready && c.Enabled && s.Ready))
		}
	}
	return append(lines, "", "Provider keys are managed centrally; client services receive scoped identities.", "Configure Adapters and grants here; select function bindings in each service's Settings.")
}

func (d *Demo) Wyvern(context.Context) (console.WyvernView, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := "ready"
	for _, item := range d.snapshot.Components {
		if item.ID == "wyvern" {
			state = item.Health
		}
	}
	return console.WyvernView{Status: console.WyvernStatus{Schema: "exocortex.wyvern.status.v1", Service: "wyvern", Version: "0.0.1", APIVersion: 1, InstanceID: "demo-host", State: state, Ready: state == "ready", Drain: state == "draining", ConfigurationLoaded: true}, Catalog: console.WyvernCatalog{
		Adapters: []console.WyvernAdapter{{ID: "google", Name: "Google", Driver: "google", Enabled: true, Profiles: []console.WyvernProfile{{Name: "default", Model: "example-model", Capabilities: []string{"text", "structured_output", "token_count"}}}}},
		Clients:  []console.WyvernClient{{ID: "mastermind", Enabled: true, Bindings: map[string]console.WyvernBinding{"crusher": {AdapterID: "google", Profile: "default"}}}, {ID: "laboratory", Enabled: true, Bindings: map[string]console.WyvernBinding{}}},
	}}, nil
}
