package routes

import (
	errors "errors"

	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
	config "vocabtrainer/server/config"
	logs "vocabtrainer/server/logs"
)

// The owner's Settings screen: config.yaml, one field per key (see
// static/js/settings.js). Both routes are the owner's alone, from a browser
// session (Guard.RequireOwner), like the activity log.
//
// What is shown and saved comes from config.Holder, which owns the file:
// GetSettings is Holder.Snapshot, SaveSettings is Holder.Save. The secret key
// is never part of either -- it is not in config.yaml, and the screen only
// says where secret.key is.

// settingsSnapshot is the whole Settings screen, as config.Holder reads it.
type settingsSnapshot config.Snapshot

func ( settingsSnapshot ) APIName() ( name string ) { name = "SettingsSnapshot"; return }

// @route("GET /api/owner/settings", summary="config.yaml, as a form",
//     tag="Owner", role=OWNER, feature="settings",
// )
func ( handlers *Handlers ) getSettings( c fiber.Ctx , in *apidoc.None ) ( reply *settingsSnapshot , err error ) {
	snapshot , err := handlers.Settings.Snapshot()
	if err != nil { return }
	shown := settingsSnapshot( snapshot )
	reply = &shown
	return
}

type saveSettingsRequest struct {
	Version string         `json:"version"`
	Changes map[string]any `json:"changes" jsonschema_description:"Setting paths and their new values."`
}

// settingsResult is what a save changed, by apply mode (config.ApplyResult).
type settingsResult struct {
	Changes   []map[string]any `json:"changes" jsonschema:"nullable"`
	Applied   []string         `json:"applied" jsonschema:"nullable"`
	Restart   []string         `json:"restart" jsonschema:"nullable"`
	Scripts   []string         `json:"scripts" jsonschema:"nullable"`
	Unchanged []string         `json:"unchanged" jsonschema:"nullable"`
	Version   string           `json:"version"`
}

func ( settingsResult ) APIName() ( name string ) { name = "SettingsResult"; return }

// saveSettings applies {version, changes: {"section.key": value}}. version
// is the one GetSettings returned; a file changed since is a 409 with the
// current version, so the page can offer to reload rather than overwrite.
// A number arrives as json.Number, so an int stays an int: a float64 would
// let 1.5 through as 1.
//
// @route("POST /api/owner/settings", summary="Save settings",
//     tag="Owner", role=OWNER, feature="settings", errors=(409,),
// )
func ( handlers *Handlers ) saveSettings( c fiber.Ctx , in *saveSettingsRequest ) ( reply *settingsResult , err error ) {
	result , save_err := handlers.Settings.Save( in.Version , in.Changes )
	var changed *config.ConflictError
	var validation *config.ValidationError
	var unknown *config.UnknownSettingError
	switch {
	case errors.As( save_err , &changed ):
		err = refuse( fiber.StatusConflict , "settings_changed" , fiber.Map{ "version": changed.Version } )
	case errors.Is( save_err , config.ErrStale ):
		err = refuse( fiber.StatusConflict , "settings_stale" )
	case errors.Is( save_err , config.ErrReadOnly ):
		err = refuse( fiber.StatusConflict , "settings_read_only" )
	case errors.Is( save_err , config.ErrFileOnly ):
		err = refuse( fiber.StatusForbidden , "settings_file_only" )
	case errors.Is( save_err , config.ErrLocked ):
		err = refuse( fiber.StatusForbidden , "settings_locked" )
	case errors.As( save_err , &validation ):
		err = refuse( fiber.StatusBadRequest , "invalid_settings" , fiber.Map{ "problems": validation.Problems } )
	case errors.As( save_err , &unknown ):
		err = refuse( fiber.StatusBadRequest , "unknown_setting" , fiber.Map{ "key": unknown.Key } )
	case errors.Is( save_err , config.ErrHandEdit ):
		err = refuse( fiber.StatusBadRequest , "settings_hand_edit" , fiber.Map{ "problems": []string{ save_err.Error() } } )
	case save_err != nil:
		err = save_err
	default:
		if len( result.Changes ) > 0 {
			handlers.audit( c , logs.KindAdmin , "settings changed" , fiber.Map{
				"changes": result.Changes,
				"restart": result.Restart,
				"scripts": result.Scripts,
			} )
		}
		reply = &settingsResult{ Applied: result.Live , Restart: result.Restart , Scripts: result.Scripts , Unchanged: result.Unchanged , Version: result.Version }
		if result.Changes != nil { reply.Changes , err = jsonObjects( result.Changes ) }
	}
	return
}
