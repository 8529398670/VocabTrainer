// Package bootstrap solves the chicken-and-egg problem this auth model
// creates: only an admin can mint a login link, and on a fresh database there
// is no admin to do it.
//
// So on first boot -- and only when no enabled admin exists -- the server
// creates one and prints a one-time login link to its own logs. Whoever can
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
func EnsureFirstAdmin( store *db.Store , cfg *config.Config , display_name string ) ( err error ) {
	exists , err := models.AnyAdminExists( store )
	if err != nil { return }
	if exists {
		return
	}

	user , err := models.CreateUser( store , display_name , models.RoleAdmin )
	if err != nil { return }

	credential , err := models.IssueLoginToken( store , user.ID , cfg.LoginTokenTTL )
	if err != nil { return }

	PrintLoginLink( cfg , fmt.Sprintf( "first-run admin %q" , display_name ) , credential )
	return
}

// PrintLoginLink writes the one moment a credential is ever visible. It is
// shown as a path because the server genuinely does not know its public
// origin -- behind a reverse proxy it sees an internal address, and printing
// a confidently wrong URL is worse than printing an obviously partial one.
func PrintLoginLink( cfg *config.Config , label string , credential string ) {
	line := strings.Repeat( "-" , 72 )
	fmt.Println( line )
	fmt.Printf( "  ONE-TIME LOGIN LINK -- %s\n" , label )
	fmt.Println( line )
	fmt.Printf( "  /login/%s\n" , credential )
	fmt.Println( line )
	fmt.Printf( "  Valid once, for %s. Prepend your real scheme and host, e.g.\n" , cfg.LoginTokenTTL )
	fmt.Printf( "    https://your-host.example.com/login/%s\n" , credential )
	fmt.Println( "  It is not stored anywhere and cannot be shown again -- if you lose" )
	fmt.Println( "  it, mint another with: vocab-trainer manage reissue-login -user-id <id>" )
	fmt.Println( line )
}
