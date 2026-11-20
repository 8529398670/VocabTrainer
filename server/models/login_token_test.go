package models

import (
	errors "errors"
	testing "testing"
	time "time"

	db "vocabtrainer/server/db"
)

var testRules = SignInRules{ SessionTTL: time.Hour }

func redeemOK( t *testing.T , store *db.Store , credential string ) {
	t.Helper()
	if _ , err := RedeemLoginToken( store , credential , testRules ); err != nil { t.Fatalf( "redeem: %v" , err ) }
}

func redeemRefused( t *testing.T , store *db.Store , credential string ) {
	t.Helper()
	if _ , err := RedeemLoginToken( store , credential , testRules ); !errors.Is( err , ErrLoginTokenInvalid ) {
		t.Fatalf( "a link that should be spent let someone in: %v" , err )
	}
}

func TestALoginLinkWorksOnceByDefault( t *testing.T ) {
	store := testStore( t )
	user , _ := CreateUser( store , "Ana" , RoleUser )
	credential , err := IssueLoginToken( store , user.ID , time.Hour )
	if err != nil { t.Fatal( err ) }
	redeemOK( t , store , credential )
	redeemRefused( t , store , credential )
	if link , _ := LiveLoginLink( store , user.ID ); link != nil { t.Fatalf( "a spent link still shows as live: %+v" , link ) }
}

func TestALoginLinkWorksUpToItsUses( t *testing.T ) {
	store := testStore( t )
	user , _ := CreateUser( store , "Ana" , RoleUser )
	credential , info , err := IssueLoginLink( store , user.ID , LoginLinkOptions{ MaxUses: 3 , TTL: time.Hour } )
	if err != nil || info.MaxUses != 3 || info.ExpiresAt == nil { t.Fatalf( "%v %+v" , err , info ) }
	for range 3 { redeemOK( t , store , credential ) }
	redeemRefused( t , store , credential )
}

func TestAnUnlimitedLinkThatNeverExpires( t *testing.T ) {
	store := testStore( t )
	user , _ := CreateUser( store , "Ana" , RoleUser )
	credential , info , err := IssueLoginLink( store , user.ID , LoginLinkOptions{ Unlimited: true } )
	if err != nil || info.Unlimited == false || info.MaxUses != 0 || info.ExpiresAt != nil { t.Fatalf( "%v %+v" , err , info ) }
	for range 5 { redeemOK( t , store , credential ) }
	link , _ := LiveLoginLink( store , user.ID )
	if link == nil || link.Uses != 5 || link.LastUsedAt == nil { t.Fatalf( "live link: %+v" , link ) }

	// Nothing about it ages out: purge leaves it alone.
	if removed , _ := PurgeExpiredLoginTokens( store ); removed != 0 { t.Fatalf( "purge removed %d" , removed ) }
	redeemOK( t , store , credential )
}

func TestAnExpiredReusableLinkIsRefused( t *testing.T ) {
	store := testStore( t )
	user , _ := CreateUser( store , "Ana" , RoleUser )
	credential , _ , err := IssueLoginLink( store , user.ID , LoginLinkOptions{ Unlimited: true , TTL: time.Hour } )
	if err != nil { t.Fatal( err ) }
	redeemOK( t , store , credential )
	ageLoginLinks( t , store , func( token *LoginToken ) { token.ExpiresAt = time.Now().UTC().Add( -time.Minute ) } )
	redeemRefused( t , store , credential )
	if removed , _ := PurgeExpiredLoginTokens( store ); removed != 1 { t.Fatalf( "purge removed %d" , removed ) }
}

// Reissue, disable, promotion, revoke and cancel each stop a reusable link
// that has already been used: what matters is whether it would still let
// someone in, not whether it is fresh.
func TestEndingAUsedReusableLink( t *testing.T ) {
	ends := map[string]func( store *db.Store , user *User , admin *User ) {
		"reissue": func( store *db.Store , user *User , _ *User ) { IssueLoginToken( store , user.ID , time.Hour ) },
		"disable": func( store *db.Store , user *User , _ *User ) { SetUserDisabled( store , user.ID , true ) },
		"promote": func( store *db.Store , user *User , admin *User ) { SetUserRole( store , user.ID , admin.ID , RoleAdmin ) },
		"revoke":  func( store *db.Store , user *User , _ *User ) { RevokeAllCredentials( store , user.ID , true ) },
		"cancel":  func( store *db.Store , user *User , _ *User ) { CancelLoginLink( store , user.ID , 0 ) },
	}
	for name , end := range ends {
		t.Run( name , func( t *testing.T ) {
			store := testStore( t )
			admin , _ := CreateUser( store , "Admin" , RoleAdmin )
			EnsureOwner( store )
			user , _ := CreateUser( store , "Ana" , RoleUser )
			credential , _ , err := IssueLoginLink( store , user.ID , LoginLinkOptions{ Unlimited: true , TTL: time.Hour } )
			if err != nil { t.Fatal( err ) }
			redeemOK( t , store , credential )
			end( store , user , admin )
			if name == "disable" { SetUserDisabled( store , user.ID , false ) }
			redeemRefused( t , store , credential )
		} )
	}
}

// A row written before links could be reused has no max_uses or uses. Used,
// it stays used; unused, it still works exactly once.
func TestOldLoginLinkRowsStillMeanOnce( t *testing.T ) {
	store := testStore( t )
	user , _ := CreateUser( store , "Ana" , RoleUser )
	credential , err := IssueLoginToken( store , user.ID , time.Hour )
	if err != nil { t.Fatal( err ) }
	ageLoginLinks( t , store , func( token *LoginToken ) { token.MaxUses = 0 } )
	redeemOK( t , store , credential )
	ageLoginLinks( t , store , func( token *LoginToken ) { token.Uses = 0 } )
	redeemRefused( t , store , credential )
}

func TestAUsedUpLinkThatNeverExpiresIsPurgedLater( t *testing.T ) {
	store := testStore( t )
	user , _ := CreateUser( store , "Ana" , RoleUser )
	credential , _ , err := IssueLoginLink( store , user.ID , LoginLinkOptions{ MaxUses: 1 } )
	if err != nil { t.Fatal( err ) }
	redeemOK( t , store , credential )
	if removed , _ := PurgeExpiredLoginTokens( store ); removed != 0 { t.Fatalf( "purged a just-used link: %d" , removed ) }
	ageLoginLinks( t , store , func( token *LoginToken ) {
		long_ago := time.Now().UTC().Add( -loginLinkRetention - time.Hour )
		token.UsedAt = &long_ago
	} )
	if removed , _ := PurgeExpiredLoginTokens( store ); removed != 1 { t.Fatalf( "purge removed %d" , removed ) }
}

func TestLoginLinkLimits( t *testing.T ) {
	store := testStore( t )
	user , _ := CreateUser( store , "Ana" , RoleUser )
	for _ , bad := range []LoginLinkOptions{
		{ MaxUses: 0 , TTL: time.Hour },
		{ MaxUses: LoginLinkMaxUses + 1 , TTL: time.Hour },
		{ MaxUses: 1 , TTL: -time.Hour },
		{ MaxUses: 1 , TTL: ( LoginLinkMaxDays + 1 ) * 24 * time.Hour },
	} {
		if _ , _ , err := IssueLoginLink( store , user.ID , bad ); !errors.Is( err , ErrBadLoginLink ) { t.Fatalf( "%+v: %v" , bad , err ) }
	}
}

func TestResolvingALoginLinkRequest( t *testing.T ) {
	once , multi := 30*time.Minute , 7*24*time.Hour
	number := func( value int ) *int { return &value }
	cases := []struct {
		request LoginLinkRequest
		want    LoginLinkOptions
	}{
		{ LoginLinkRequest{} , LoginLinkOptions{ MaxUses: 1 , TTL: once } },
		{ LoginLinkRequest{ MaxUses: number( 5 ) } , LoginLinkOptions{ MaxUses: 5 , TTL: multi } },
		{ LoginLinkRequest{ MaxUses: number( 0 ) } , LoginLinkOptions{ Unlimited: true , TTL: multi } },
		{ LoginLinkRequest{ ExpiresInMinutes: number( 60 ) } , LoginLinkOptions{ MaxUses: 1 , TTL: time.Hour } },
		{ LoginLinkRequest{ MaxUses: number( 0 ) , ExpiresInMinutes: number( 0 ) } , LoginLinkOptions{ Unlimited: true } },
	}
	for _ , item := range cases {
		got , err := ResolveLoginLink( item.request , once , multi )
		if err != nil || got != item.want { t.Errorf( "%+v: got %+v %v, want %+v" , item.request , got , err , item.want ) }
	}
	for _ , bad := range []LoginLinkRequest{
		{ MaxUses: number( -1 ) },
		{ MaxUses: number( 1001 ) },
		{ ExpiresInMinutes: number( -5 ) },
		{ ExpiresInMinutes: number( 525601 ) },
	} {
		if _ , err := ResolveLoginLink( bad , once , multi ); !errors.Is( err , ErrBadLoginLink ) { t.Errorf( "%+v: %v" , bad , err ) }
	}
}

func TestParsingLoginLinkFlags( t *testing.T ) {
	// -1 stands for "left out".
	show := func( value *int ) ( result int ) {
		result = -1
		if value != nil { result = *value }
		return
	}
	good := map[[2]string][2]int{
		{ "" , "" }:               { -1 , -1 },
		{ "3" , "30m" }:           { 3 , 30 },
		{ "unlimited" , "7d" }:    { 0 , 10080 },
		{ "Unlimited" , "never" }: { 0 , 0 },
		{ "" , "12h" }:            { -1 , 720 },
	}
	for input , want := range good {
		request , err := ParseLoginLinkFlags( input[ 0 ] , input[ 1 ] )
		if err != nil { t.Fatalf( "%v: %v" , input , err ) }
		if got := [2]int{ show( request.MaxUses ) , show( request.ExpiresInMinutes ) }; got != want {
			t.Errorf( "%v: got %v, want %v" , input , got , want )
		}
	}
	for _ , bad := range [][2]string{ { "0" , "" } , { "1001" , "" } , { "lots" , "" } , { "" , "7" } , { "" , "7w" } , { "" , "366d" } , { "" , "0m" } } {
		if _ , err := ParseLoginLinkFlags( bad[ 0 ] , bad[ 1 ] ); err == nil { t.Errorf( "%v accepted" , bad ) }
	}
}

// ageLoginLinks rewrites every stored link, to stand in for time passing or
// for a row an older build wrote.
func ageLoginLinks( t *testing.T , store *db.Store , change func( token *LoginToken ) ) {
	t.Helper()
	err := store.Write( func( tx *db.Tx ) ( tx_err error ) {
		type row struct {
			key   []byte
			token *LoginToken
		}
		rows := []row{}
		tx_err = tx.ForEach( db.BucketLoginTokens , func() any { return &LoginToken{} } , func( key []byte , item any ) bool {
			rows = append( rows , row{ key , item.( *LoginToken ) } )
			return true
		} )
		for _ , each := range rows {
			change( each.token )
			if tx_err = tx.Put( db.BucketLoginTokens , each.key , each.token ); tx_err != nil { return }
		}
		return
	} )
	if err != nil { t.Fatal( err ) }
}
