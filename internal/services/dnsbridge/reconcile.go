package dnsbridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

var ErrConflict = errors.New("DNS ownership or current-value conflict")

type Snapshot struct {
	Controller Controller
	Records    map[RecordKey]Endpoint
	Names      map[string]bool
	Foreign    map[string]bool
}

type Mutation struct {
	Names  []string
	Before map[RecordKey]Endpoint
	After  map[RecordKey]Endpoint
}

type Backend interface {
	Records(context.Context) (Snapshot, error)
	Apply(context.Context, Snapshot, Mutation) error
}

type operation struct {
	old *Endpoint
	new *Endpoint
}

func (p *Policy) plan(snapshot Snapshot, changes Changes) (Mutation, error) {
	mutation := Mutation{Before: make(map[RecordKey]Endpoint), After: make(map[RecordKey]Endpoint)}
	if len(changes.UpdateOld) != len(changes.UpdateNew) {
		return mutation, errors.New("updates require paired old and new records")
	}
	operations := make(map[RecordKey]operation)
	applications := make(map[string]bool)
	add := func(old, desired *Endpoint) error {
		var op operation
		var key RecordKey
		if old != nil {
			normalized, err := p.normalize(*old, false)
			if err != nil {
				return err
			}
			op.old, key = &normalized, normalized.key()
		}
		if desired != nil {
			normalized, err := p.normalize(*desired, true)
			if err != nil {
				return err
			}
			if old != nil && key != normalized.key() {
				return errors.New("updates cannot change a record's name or type")
			}
			op.new, key = &normalized, normalized.key()
		}
		if _, duplicate := operations[key]; duplicate {
			return errors.New("changes contain multiple operations for the same record")
		}
		application, err := p.applicationName(key.Name, key.Type)
		if err != nil {
			return err
		}
		applications[application] = true
		operations[key] = op
		return nil
	}
	for i := range changes.Create {
		if err := add(nil, &changes.Create[i]); err != nil {
			return mutation, err
		}
	}
	for i := range changes.UpdateOld {
		if err := add(&changes.UpdateOld[i], &changes.UpdateNew[i]); err != nil {
			return mutation, err
		}
	}
	for i := range changes.Delete {
		if err := add(&changes.Delete[i], nil); err != nil {
			return mutation, err
		}
	}
	if len(operations) > 1000 {
		return mutation, errors.New("too many record operations")
	}

	for application := range applications {
		keys := p.applicationKeys(application)
		if p.applicationConflict(snapshot, application) {
			return mutation, fmt.Errorf("%w: %s is not owned by this bridge", ErrConflict, application)
		}
		for _, key := range keys {
			if current, exists := snapshot.Records[key]; exists {
				mutation.Before[key] = current
				mutation.After[key] = current
			}
		}
		for _, key := range keys {
			op, requested := operations[key]
			if !requested {
				continue
			}
			current, exists := snapshot.Records[key]
			// A fresh authenticated snapshot, not a successful send, establishes retry completion.
			if op.new != nil && exists && sameRecord(current, *op.new) {
				continue
			}
			if op.new == nil && !exists {
				continue
			}
			if op.old == nil {
				if exists {
					return mutation, fmt.Errorf("%w: create would replace %s", ErrConflict, key.Name)
				}
			} else if !exists || !sameValues(current, *op.old) {
				return mutation, fmt.Errorf("%w: expected record changed at %s", ErrConflict, key.Name)
			}
			if op.new == nil {
				delete(mutation.After, key)
			} else {
				mutation.After[key] = *op.new
			}
		}
		for i := 0; i < 2; i++ {
			_, hasAddress := mutation.After[keys[i]]
			owner, hasOwner := mutation.After[keys[i+2]]
			if hasAddress != hasOwner || (hasOwner && !p.owned(owner)) {
				return mutation, errors.New("each address family and its valid ownership TXT must remain paired")
			}
		}
		mutation.Names = append(mutation.Names, application, keys[2].Name, keys[3].Name)
	}
	slices.Sort(mutation.Names)
	return mutation, nil
}

func (m Mutation) empty() bool {
	if len(m.Before) != len(m.After) {
		return false
	}
	for key, before := range m.Before {
		after, exists := m.After[key]
		if !exists || !sameRecord(before, after) {
			return false
		}
	}
	return true
}
