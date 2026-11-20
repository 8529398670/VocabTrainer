package routes

import (
	fmt "fmt"
	http "net/http"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	models "vocabtrainer/server/models"
)

func TestCutPageCutsAndPointsAtTheNextPage( t *testing.T ) {
	rows := []int{ 1 , 2 , 3 , 4 , 5 }
	cases := []struct {
		limit , offset int
		count          int
		next           any
	}{
		{ 2 , 0 , 2 , 2 },
		{ 2 , 2 , 2 , 4 },
		{ 2 , 4 , 1 , nil },
		{ 5 , 0 , 5 , nil },
		{ 2 , 99 , 0 , nil },
	}
	for _ , item := range cases {
		cut , paged := cutPage( rows , Page{ Limit: item.limit , Offset: item.offset } )
		var next any
		if paged.Next != nil { next = *paged.Next }
		if len( cut ) != item.count {
			t.Fatalf( "limit %d offset %d: %d rows" , item.limit , item.offset , len( cut ) )
		}
		if next != item.next || paged.Total != 5 {
			t.Fatalf( "limit %d offset %d: %v %v" , item.limit , item.offset , next , paged.Total )
		}
	}
}

func TestListRoutesPage( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	for index := 0; index < 5; index++ {
		models.CreateUser( fixture.store , fmt.Sprintf( "Person %d" , index ) , models.RoleUser )
	}

	status , first := owner.do( t , http.MethodGet , "/api/admin/users?limit=3" , nil )
	if status != http.StatusOK || len( first[ "users" ].( []any ) ) != 3 || first[ "next" ] != float64( 3 ) || first[ "total" ] != float64( 7 ) {
		t.Fatalf( "first page: %d %v" , status , first )
	}
	_ , last := owner.do( t , http.MethodGet , "/api/admin/users?limit=3&offset=6" , nil )
	if len( last[ "users" ].( []any ) ) != 1 || last[ "next" ] != nil {
		t.Fatalf( "last page: %v" , last )
	}
	_ , past := owner.do( t , http.MethodGet , "/api/admin/users?offset=50" , nil )
	if rows , ok := past[ "users" ].( []any ); ok == false || len( rows ) != 0 {
		t.Fatalf( "a page past the end should be an empty list: %v" , past )
	}

	// Each list answers under its own name, with no arguments too.
	for path , name := range map[ string ]string{
		"/api/team":               "members",
	} {
		status , reply := owner.do( t , http.MethodGet , path , nil )
		if _ , ok := reply[ name ].( []any ); status != http.StatusOK || ok == false {
			t.Fatalf( "%s: %d %v" , path , status , reply )
		}
	}
}

func TestListRoutesRefuseABadPage( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	for _ , query := range []string{ "limit=0" , "limit=1001" , "limit=x" , "offset=-1" , "offset=x" } {
		if status , _ := owner.do( t , http.MethodGet , "/api/admin/users?"+query , nil ); status != http.StatusBadRequest {
			t.Fatalf( "%s: status %d" , query , status )
		}
	}
}


func TestMatchRowsLooksInTheNamedFieldsOnly( t *testing.T ) {
	rows := []fiber.Map{
		{ "id": 1 , "name": "Team Chat" , "url": "https://a.example" , "note": "zebra" },
		{ "id": 2 , "name": "alerts" , "url": "https://chat.example" , "size": 5 },
		{ "id": 3 , "name": "backups" , "url": "https://b.example" },
	}
	cases := []struct {
		query string
		want  []int
	}{
		{ "" , []int{ 1 , 2 , 3 } },
		{ "   " , []int{ 1 , 2 , 3 } },
		{ "CHAT" , []int{ 1 , 2 } },    // any case, and either field
		{ " backups " , []int{ 3 } },   // the spaces around it do not count
		{ "zebra" , []int{} },          // a field it was not told to look in
		{ "5" , []int{} },              // nor a number
	}
	for _ , item := range cases {
		got := []int{}
		for _ , row := range matchRows( rows , item.query , "name" , "url" ) {
			got = append( got , row[ "id" ].( int ) )
		}
		if fmt.Sprint( got ) != fmt.Sprint( item.want ) { t.Errorf( "%q: %v, want %v" , item.query , got , item.want ) }
	}
}


func TestTheUsersListSaysWhoAndSince( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	_ , page := owner.do( t , http.MethodGet , "/api/admin/users?sort=created_at&dir=desc" , nil )
	rows , _ := page[ "users" ].( []any )
	if len( rows ) < 2 { t.Fatalf( "%v" , page ) }
	row := rows[ 0 ].( map[ string ]any )
	if row[ "created_at" ] == nil { t.Fatalf( "%v" , row ) }
	if _ , present := row[ "password_hash" ]; present { t.Fatalf( "%v" , row ) }
}


func TestSortRowsPutsEachKindInOrder( t *testing.T ) {
	early := time.Date( 2026 , 1 , 1 , 0 , 0 , 0 , 0 , time.UTC )
	late := early.Add( time.Hour )
	var unset *time.Time
	rows := []fiber.Map{
		{ "id": 1 , "name": "bob" , "size": 30 , "on": true , "seen": &late },
		{ "id": 2 , "name": "Alice" , "size": 4 , "on": false , "seen": unset },
		{ "id": 3 , "name": "" , "size": 200 , "on": true , "seen": &early },
		{ "id": 4 , "name": "carol" , "size": 4 , "on": false },
	}
	cases := []struct {
		field      string
		descending bool
		want       []int
	}{
		{ "name" , false , []int{ 2 , 1 , 4 , 3 } },  // any case, nothing goes last
		{ "name" , true , []int{ 4 , 1 , 2 , 3 } },   // and last this way too
		{ "size" , false , []int{ 2 , 4 , 1 , 3 } },  // numbers, not text; a tie keeps its order
		{ "size" , true , []int{ 3 , 1 , 2 , 4 } },   // a tie keeps it this way too
		{ "on" , true , []int{ 1 , 3 , 2 , 4 } },
		{ "seen" , false , []int{ 3 , 1 , 2 , 4 } },  // never seen is not the oldest
		{ "seen" , true , []int{ 1 , 3 , 2 , 4 } },
	}
	for _ , item := range cases {
		got := []int{}
		for _ , row := range sortRows( rows , item.field , item.descending ) {
			got = append( got , row[ "id" ].( int ) )
		}
		if fmt.Sprint( got ) != fmt.Sprint( item.want ) {
			t.Errorf( "%s desc=%v: %v, want %v" , item.field , item.descending , got , item.want )
		}
	}
}

func TestAListSortsBeforeItIsCut( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	for _ , name := range []string{ "Yara" , "aaron" , "Zed" } {
		if _ , err := models.CreateUser( fixture.store , name , models.RoleUser ); err != nil { t.Fatal( err ) }
	}
	status , page := owner.do( t , http.MethodGet , "/api/admin/users?sort=display_name&limit=1" , nil )
	rows , _ := page[ "users" ].( []any )
	if status != 200 || len( rows ) != 1 || rows[ 0 ].( map[ string ]any )[ "display_name" ] != "aaron" { t.Fatalf( "%d %v" , status , page ) }
	_ , page = owner.do( t , http.MethodGet , "/api/admin/users?sort=display_name&dir=desc&limit=1" , nil )
	rows , _ = page[ "users" ].( []any )
	if rows[ 0 ].( map[ string ]any )[ "display_name" ] != "Zed" { t.Fatalf( "%v" , page ) }

	for _ , query := range []string{ "sort=password_hash" , "sort=id" , "sort=display_name&dir=up" } {
		if status , _ = owner.do( t , http.MethodGet , "/api/admin/users?"+query , nil ); status != 400 { t.Fatalf( "%s: %d" , query , status ) }
	}
}


// Without the guard the static catch-all would answer a mistyped endpoint.
func TestAnUnknownAPIPathIsAJSON404( t *testing.T ) {
	fixture := newAuthFixture( t )
	status , reply := fixture.signIn( t , fixture.owner.ID ).do( t , http.MethodGet , "/api/nope" , nil )
	if status != http.StatusNotFound || reply[ "error" ] != "not found" {
		t.Fatalf( "%d %v" , status , reply )
	}
}
