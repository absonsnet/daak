package backend

import (
	"database/sql"
	"fmt"

	"github.com/google/uuid"
)

// Local application of scoped (this / this-and-future) instance edits.
//
// The API's persistThis* methods and the offline queue share these: the
// API calls them after a successful push, updateInstance calls them to
// soft-commit an edit whose push failed (audit4 finding 20), and Drain
// calls them after a successful replay (findings 20 and 28), so the
// local rows reflect the change without waiting for the next sync
// (sync runs before Drain in a pass, so a replay that only pushed
// remotely would leave the event visibly unchanged until the next
// sync). The bodies are the former persistThis* implementations,
// unchanged apart from taking the store explicitly.

func applyThisUpdateLocal(store *Store, master Event, in EventInput, instanceUnix int64, result PushInstanceResult) error {
	overrideBlob, err := serializeVEVENTWithRecurrenceID(master.UID, in, instanceUnix)
	if err != nil {
		return fmt.Errorf("serialize override: %w", err)
	}
	return store.WithTx(func(tx *sql.Tx) error {
		if err := store.UpsertOverrideTx(tx, master.ID, instanceUnix, overrideBlob); err != nil {
			return err
		}
		if result.MasterNewETag != "" {
			if _, err := tx.Exec(
				`UPDATE events SET etag = ? WHERE id = ?`,
				result.MasterNewETag, master.ID,
			); err != nil {
				return fmt.Errorf("update master etag: %w", err)
			}
		}
		return extractAndUpsertAlarmsTx(store, tx, master)
	})
}

func applyThisDeleteLocal(store *Store, master Event, instanceUnix int64, result PushInstanceResult) error {
	updatedICS, err := addEXDATE(master.ICSBlob, instanceUnix)
	if err != nil {
		return err
	}
	updated := master
	updated.ICSBlob = updatedICS
	if result.MasterNewETag != "" {
		updated.ETag = result.MasterNewETag
	}
	return store.WithTx(func(tx *sql.Tx) error {
		if err := store.UpsertEventTx(tx, updated); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`DELETE FROM event_recurrence_overrides WHERE event_id = ? AND recurrence_id_unix = ?`,
			master.ID, instanceUnix,
		); err != nil {
			return err
		}
		return extractAndUpsertAlarmsTx(store, tx, updated)
	})
}

func applyThisAndFutureUpdateLocal(store *Store, master Event, in EventInput, splitUnix int64, result PushInstanceResult) error {
	// splitUnix is the ORIGINAL occurrence start — the clamp point.
	// in.DTStartUnix is the NEW series' start and may have moved.

	// 1. Clamp master's RRULE locally.
	clampedRRULE := clampRRuleUntil(master.RRuleText, splitUnix-1)
	clampedICS, err := reserializeMasterICS(master, clampedRRULE)
	if err != nil {
		return err
	}
	clampedMaster := master
	clampedMaster.RRuleText = clampedRRULE
	clampedMaster.ICSBlob = clampedICS
	if result.MasterNewETag != "" {
		clampedMaster.ETag = result.MasterNewETag
	}

	// 2. New master for the future series — UID + identifiers from the
	//    provider's NewSeries (non-nil for any non-local source), or
	//    locally-generated for local.
	var newUID string
	var newETag string
	var newHref string
	var newProviderEventID string
	if result.NewSeries != nil {
		newUID = result.NewSeries.UID
		newETag = result.NewSeries.ETag
		newHref = result.NewSeries.Href
		newProviderEventID = result.NewSeries.ProviderEventID
	}
	if newUID == "" {
		newUID = uuid.NewString() + "@daak-local"
	}
	newICS, err := serializeVEVENT(newUID, in)
	if err != nil {
		return err
	}
	newMaster := Event{
		ID:              uuid.NewString(),
		CalendarID:      master.CalendarID,
		UID:             newUID,
		ETag:            newETag,
		Href:            newHref,
		ProviderEventID: newProviderEventID,
		Summary:         in.Summary,
		Description:     in.Description,
		Location:        in.Location,
		DTStartUnix:     in.DTStartUnix,
		DTEndUnix:       in.DTEndUnix,
		IsAllDay:        in.IsAllDay,
		RRuleText:       rruleText(in.Recurrence),
		Transparency:    normTransparency(in.Transparency),
		Visibility:      normVisibility(in.Visibility),
		ICSBlob:         newICS,
	}

	return store.WithTx(func(tx *sql.Tx) error {
		if err := store.UpsertEventTx(tx, clampedMaster); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`DELETE FROM event_recurrence_overrides WHERE event_id = ? AND recurrence_id_unix >= ?`,
			master.ID, splitUnix,
		); err != nil {
			return err
		}
		if err := store.UpsertEventTx(tx, newMaster); err != nil {
			return err
		}
		if err := extractAndUpsertAlarmsTx(store, tx, clampedMaster); err != nil {
			return err
		}
		return extractAndUpsertAlarmsTx(store, tx, newMaster)
	})
}

func applyThisAndFutureDeleteLocal(store *Store, master Event, splitUnix int64, result PushInstanceResult) error {
	clampedRRULE := clampRRuleUntil(master.RRuleText, splitUnix-1)
	clampedICS, err := reserializeMasterICS(master, clampedRRULE)
	if err != nil {
		return err
	}
	updated := master
	updated.RRuleText = clampedRRULE
	updated.ICSBlob = clampedICS
	if result.MasterNewETag != "" {
		updated.ETag = result.MasterNewETag
	}
	return store.WithTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(
			`DELETE FROM event_recurrence_overrides WHERE event_id = ? AND recurrence_id_unix >= ?`,
			master.ID, splitUnix,
		); err != nil {
			return err
		}
		if err := store.UpsertEventTx(tx, updated); err != nil {
			return err
		}
		return extractAndUpsertAlarmsTx(store, tx, updated)
	})
}
