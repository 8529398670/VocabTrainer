package models

import (
	binary "encoding/binary"
	sort "sort"
	time "time"

	db "vocabtrainer/server/db"
)

// DayStat is one user's activity on one day.
//
// Aggregates rather than a review log. A log would answer more questions, but
// it grows without bound and nothing the app shows -- today's count, the
// streak, the last few weeks -- needs individual reviews back. One record per
// active day per user is a few hundred bytes a year.
type DayStat struct {
	Date    string `json:"date"` // YYYY-MM-DD, in the user's local day
	Known   int    `json:"known"`
	Unknown int    `json:"unknown"`
	Skipped int    `json:"skipped"`
	New     int    `json:"new"`
}

func ( stat *DayStat ) Reviewed() ( result int ) {
	result = stat.Known + stat.Unknown + stat.Skipped
	return
}

func dailyKey( user_id uint64 , date string ) ( key []byte ) {
	key = make( []byte , 8 , 8+len( date ) )
	binary.BigEndian.PutUint64( key , user_id )
	key = append( key , date... )
	return
}

// DateKey formats a day the way the bucket keys it. Days are the user's
// local days: a session at 11pm belongs to that evening, not to tomorrow in
// UTC, and a streak that breaks at midnight UTC would be wrong for most of
// the world.
func DateKey( when time.Time ) ( result string ) {
	result = when.Format( "2006-01-02" )
	return
}

// RecordActivity bumps one day's counters. The whole update happens inside a
// single transaction so concurrent swipes cannot lose a count.
func RecordActivity( store *db.Store , user_id uint64 , date string , outcome string , isNew bool ) ( err error ) {
	key := dailyKey( user_id , date )

	// Three counters for what is now five answers, because the counters are
	// the three lists a word can be in and the four grades only say how
	// firmly. Hard, Good and Easy all mean the word was recalled, so all
	// three land where "I know it" lands -- which is also the status the
	// scheduler gives the card, so the daily count and the list agree.
	bump := func( stat *DayStat ) {
		switch outcome {
		case OutcomeKnown , OutcomeHard , OutcomeEasy:
			stat.Known += 1
		case OutcomeUnknown:
			stat.Unknown += 1
		case OutcomeSkip:
			stat.Skipped += 1
		}
		if isNew { stat.New += 1 }
	}

	fresh := &DayStat{ Date: date }
	bump( fresh )
	written , err := store.PutIfAbsent( db.BucketDailyStats , key , fresh )
	if err != nil || written { return }

	err = store.UpdateValue( db.BucketDailyStats , key ,
		func() any { return &DayStat{} } ,
		func( item any ) ( mutate_err error ) {
			bump( item.( *DayStat ) )
			return
		} )
	return
}

func GetDayStat( store *db.Store , user_id uint64 , date string ) ( stat *DayStat , err error ) {
	stat = &DayStat{ Date: date }
	get_err := store.Get( db.BucketDailyStats , dailyKey( user_id , date ) , stat )
	if get_err != nil && get_err != db.ErrNotFound {
		err = get_err
		stat = nil
	}
	return
}

// ListDayStats returns a user's active days, oldest first.
func ListDayStats( store *db.Store , user_id uint64 ) ( stats []*DayStat , err error ) {
	stats = []*DayStat{}
	prefix := make( []byte , 8 )
	binary.BigEndian.PutUint64( prefix , user_id )
	err = store.ForEachPrefix( db.BucketDailyStats , prefix ,
		func() any { return &DayStat{} } ,
		func( key []byte , item any ) bool {
			stats = append( stats , item.( *DayStat ) )
			return true
		} )
	if err != nil { return }
	sort.Slice( stats , func( a int , b int ) bool { return stats[ a ].Date < stats[ b ].Date } )
	return
}

// Streak counts consecutive days with at least one review, ending today or
// yesterday.
//
// Yesterday counts as still alive on purpose: a streak that dies at midnight
// punishes someone who studies each evening and happens to open the app a
// little later one night. It breaks only once a whole day has passed with
// nothing in it.
func Streak( stats []*DayStat , today time.Time ) ( result int ) {
	active := map[string]bool{}
	for _ , stat := range stats {
		if stat.Reviewed() > 0 { active[ stat.Date ] = true }
	}
	if len( active ) == 0 { return }

	cursor := today
	if active[ DateKey( cursor ) ] == false {
		cursor = cursor.AddDate( 0 , 0 , -1 )
		if active[ DateKey( cursor ) ] == false { return }
	}
	for active[ DateKey( cursor ) ] {
		result += 1
		cursor = cursor.AddDate( 0 , 0 , -1 )
	}
	return
}

// ResetStats clears a user's daily history, alongside ResetProgress.
func ResetStats( store *db.Store , user_id uint64 ) ( removed int , err error ) {
	prefix := make( []byte , 8 )
	binary.BigEndian.PutUint64( prefix , user_id )
	removed , err = store.DeleteWhere( db.BucketDailyStats ,
		func() any { return &DayStat{} } ,
		func( key []byte , item any ) bool {
			return len( key ) >= 8 && string( key[ :8 ] ) == string( prefix )
		} )
	return
}
