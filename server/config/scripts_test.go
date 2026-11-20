package config

import (
	os "os"
	exec "os/exec"
	filepath "path/filepath"
	runtime "runtime"
	testing "testing"
)

// S5: scripts/run.sh keeps .run/ and its log readable by this account alone,
// whatever the umask -- a first run's log holds the owner's login link. A
// .run/ left open by an older run.sh is closed too.
func TestRunDirIsPrivate( t *testing.T ) {
	if runtime.GOOS == "windows" { t.Skip( "run.sh is for unix shells" ) }
	lib , err := filepath.Abs( "../../scripts/lib.sh" )
	if err != nil { t.Fatal( err ) }
	for _ , existing := range []bool{ false , true } {
		dir := t.TempDir()
		run_dir := filepath.Join( dir , ".run" )
		log_file := filepath.Join( run_dir , "server.log" )
		if existing {
			os.MkdirAll( run_dir , 0o755 )
			os.WriteFile( log_file , []byte( "old" ) , 0o644 )
			os.Chmod( run_dir , 0o755 )
			os.Chmod( log_file , 0o644 )
		}
		command := exec.Command( "bash" , "-c" , `umask 022 && source "$1" && private_run_dir` , "bash" , lib )
		command.Dir = dir
		if output , run_err := command.CombinedOutput(); run_err != nil { t.Fatalf( "%v: %s" , run_err , output ) }
		for path , want := range map[string]os.FileMode{ run_dir: 0o700 , log_file: 0o600 } {
			info , stat_err := os.Stat( path )
			if stat_err != nil { t.Fatal( stat_err ) }
			if info.Mode().Perm() != want { t.Errorf( "existing %v: %s is %o, want %o" , existing , path , info.Mode().Perm() , want ) }
		}
	}
}

// F12: config.yaml is writable from inside the container, so a docker:
// section that changed since the last start waits for a yes at the host.
// .run/docker.last is the copy that counts; the container never sees it.
func TestDockerChangesWaitForAYes( t *testing.T ) {
	if runtime.GOOS == "windows" { t.Skip( "docker/run.sh is for unix shells" ) }
	lib , err := filepath.Abs( "../../docker/lib.sh" )
	if err != nil { t.Fatal( err ) }
	const was = "docker_container='app'"
	const now = "docker_container='someone-elses'"
	cases := []struct {
		name   string
		last   string // "" means no .run/docker.last yet
		accept string
		ok     bool
		after  string
	}{
		{ "first start" , "" , "false" , true , "" },
		{ "unchanged" , now , "false" , true , now },
		{ "changed, no terminal, no flag" , was , "false" , false , was },
		{ "changed, --accept-docker-changes" , was , "true" , true , now },
	}
	for _ , test := range cases {
		dir := t.TempDir()
		last_file := filepath.Join( dir , ".run" , "docker.last" )
		if test.last != "" {
			os.MkdirAll( filepath.Dir( last_file ) , 0o700 )
			os.WriteFile( last_file , []byte( test.last + "\n" ) , 0o600 )
		}
		command := exec.Command( "bash" , "-c" ,
			`set -euo pipefail; source "$1"; DOCKER_SETTINGS="$2"; ACCEPT_DOCKER_CHANGES="$3"; confirm_docker_changes` ,
			"bash" , lib , now , test.accept )
		command.Dir = dir
		output , run_err := command.CombinedOutput()
		if ( run_err == nil ) != test.ok { t.Errorf( "%s: ok = %v, want %v: %s" , test.name , run_err == nil , test.ok , output ) }
		if test.after != "" {
			got , _ := os.ReadFile( last_file )
			if string( got ) != test.after + "\n" { t.Errorf( "%s: docker.last = %q, want %q" , test.name , got , test.after ) }
		}
	}
}
