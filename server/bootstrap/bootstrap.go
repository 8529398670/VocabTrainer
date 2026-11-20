// Package bootstrap solves the chicken-and-egg problem this auth model
// creates: only an admin can mint a login link, and on a fresh database there
// is no admin to do it.
//
// So on first boot -- and only when no enabled admin exists -- the server
// creates one and prints a login link (one-time) to its own logs. Whoever can
// read the container logs is already trusted with the deployment, so this
// grants nothing they did not already have, and it means a fresh deploy is
// usable without a separate setup command.
package bootstrap

import (
	fmt "fmt"
	strings "strings"

	config "vocabtrainer/server/config"
	db "vocabtrainer/server/db"
	models "vocabtrainer/server/models"
)

// EnsureFirstAdmin is a no-op once any enabled admin exists, so restarting
// the container does not keep minting new admin accounts.
//
// It also adopts an owner for a database written before the owner marker
// existed (see models.EnsureOwner). A fresh database needs no adoption: the
// admin created below becomes the owner as it is created.
func EnsureFirstAdmin( store *db.Store , cfg *config.Config , display_name string ) ( err error ) {
	if _ , err = models.EnsureOwner( store ); err != nil { return }

	exists , err := models.AnyAdminExists( store )
	if err != nil { return }
	if exists {
		return
	}

	user , err := models.CreateUser( store , display_name , models.RoleAdmin )
	if err != nil { return }

	credential , info , err := models.IssueLoginLink( store , user.ID , models.OnceLink( cfg.LoginTokenTTL ) )
	if err != nil { return }

	PrintLoginLink( cfg , fmt.Sprintf( "first-run admin %q" , display_name ) , credential , &info )
	return
}

// PrintAPIKey writes the one moment an API key is ever visible. It lives
// beside PrintLoginLink so that every path which reveals a credential is in
// one file, with one shape and one warning.
//
// Unlike a login link this is a complete credential, not a URL; the example
// request uses the same address the links do (see linkNote).
func PrintAPIKey( cfg *config.Config , label string , credential string , expires string ) {
	line := strings.Repeat( "-" , 72 )
	fmt.Println( line )
	fmt.Printf( "  API KEY -- %s\n" , label )
	fmt.Println( line )
	fmt.Printf( "  %s\n" , credential )
	fmt.Println( line )
	fmt.Printf( "  Expires: %s\n" , expires )
	fmt.Println( "  Send it as a header:" )
	fmt.Printf( "    curl -H \"Authorization: Bearer %s\" %s/api/me\n" , credential , cfg.URL )
	linkNote( cfg )
	fmt.Println( "  It is not stored anywhere and cannot be shown again. If it leaks," )
	fmt.Println( "  revoke it: vocabtrainer manage revoke-key -key-id <id>" )
	fmt.Println( line )
}

// PrintLoginLink writes the one moment a credential is ever visible, as one
// full URL you can click. The address is server.public_url when set; without
// it the server only knows its own listen address, which linkNote says
// rather than leaving a confidently wrong host unexplained.
//
// The header says LOGIN LINK whatever the link allows; scripts/login.sh and
// docker/run.sh find the link by it.
func PrintLoginLink( cfg *config.Config , label string , credential string , link *models.LoginLinkInfo ) {
	line := strings.Repeat( "-" , 72 )
	fmt.Println( line )
	fmt.Printf( "  LOGIN LINK -- %s\n" , label )
	fmt.Println( line )
	fmt.Printf( "  %s/login/%s\n" , cfg.URL , credential )
	fmt.Println( line )
	if link != nil {
		fmt.Printf( "  %s\n" , models.DescribeLoginLink( *link ) )
	}
	linkNote( cfg )
	fmt.Println( "  It is not stored anywhere and cannot be shown again -- if you lose" )
	fmt.Println( "  it, mint another with scripts/login.sh (or: vocab-trainer manage" )
	fmt.Println( "  reissue-login -user-id <id>)." )
	fmt.Println( line )
}


// linkNote says where the address in a printed link came from, so a link
// built from 127.0.0.1 is not mistaken for one other people can open.
func linkNote( cfg *config.Config ) {
	fmt.Printf( "  Address from %s.\n" , cfg.URLSource )
}
