package jobs

import (
	errors "errors"
	fmt "fmt"
	time "time"

	config "vocabtrainer/server/config"
	db "vocabtrainer/server/db"
	logs "vocabtrainer/server/logs"
	models "vocabtrainer/server/models"
)

const housekeepingInterval = time.Hour

// Housekeeping registers the jobs every app has. prune-logs is left out when
// the activity log is off: logs.enabled needs a restart, so it cannot appear
// later.
func Housekeeping( registry *Registry , store *db.Store , logger *logs.Logger , settings *config.Holder ) {
	registry.Register( "purge" , housekeepingInterval , purge( store ) )
	if logger.Enabled() {
		registry.Register( "prune-logs" , housekeepingInterval , pruneLogs( logger ) )
	}
}

// purge drops expired sessions, login links and sign-in challenges, the API
// keys and invite links that have been dead long enough to stop being worth
// listing, and old webhook rows. None of it is a correctness issue -- every one of those is
// already refused on read -- so this only stops the bolt file growing forever.
// Every step runs whatever happens to the one before it.
//
// Its first step is the one exception: accounts whose expiry has passed are
// disabled and their access revoked (models.ExpireAccounts). They already
// stopped working when the time passed; this makes it stick.
func purge( store *db.Store ) ( run Func ) {
	run = func() ( note string , err error ) {
		expired , expired_err := models.ExpireAccounts( store )
		sessions , sessions_err := models.PurgeExpiredSessions( store )
		tokens , tokens_err := models.PurgeExpiredLoginTokens( store )
		note = fmt.Sprintf( "expired %d accounts, removed %d sessions, %d login links" , expired , sessions , tokens )
		err = errors.Join( expired_err , sessions_err , tokens_err )
		keys , keys_err := models.PurgeDeadAPIKeys( store )
		note += fmt.Sprintf( ", %d keys" , keys )
		err = errors.Join( err , keys_err )
		return
	}
	return
}

// pruneLogs is the activity log's retention: compress, expire, recycle.
func pruneLogs( logger *logs.Logger ) ( run Func ) {
	run = func() ( note string , err error ) {
		pruned , err := logger.Prune()
		if err != nil { return }
		note = fmt.Sprintf( "%d days deleted, %d compressed" , pruned.Deleted , pruned.Compressed )
		if pruned.Deleted > 0 || pruned.Compressed > 0 {
			logger.System( logs.LevelInfo , "logs pruned" , map[string]any{ "deleted": pruned.Deleted , "compressed": pruned.Compressed } )
		}
		return
	}
	return
}

