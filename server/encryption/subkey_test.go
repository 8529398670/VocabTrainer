package encryption

import (
	hex "encoding/hex"
	testing "testing"
)

// S12: each purpose seals with its own subkey of secret.key, so a value sealed
// for one never opens as another: a session cookie is not a log line, and
// neither opens with secret.key itself.
func TestAValueSealedForOnePurposeDoesNotOpenForAnother( t *testing.T ) {
	secret := GenerateKeyHex()
	cookie , err := ChaChaEncryptString( SubKey( secret , PurposeCookie ) , "session credential" )
	if err != nil { t.Fatal( err ) }
	if _ , err = ChaChaDecryptBase64String( SubKey( secret , PurposeLogs ) , cookie ); err == nil { t.Error( "a cookie opened as a log line" ) }
	if _ , err = ChaChaDecryptBase64String( secret , cookie ); err == nil { t.Error( "a cookie opened with secret.key itself" ) }
	if opened , err := ChaChaDecryptBase64String( SubKey( secret , PurposeCookie ) , cookie ); err != nil || opened != "session credential" { t.Errorf( "%q %v" , opened , err ) }

	seen := map[ string ]bool{ secret: true }
	for _ , purpose := range []string{ PurposeDB , PurposeLogs , PurposeCookie , PurposeTOTP , PurposeFiles , PurposeWebhooks } {
		key := SubKey( secret , purpose )
		if len( key ) != 64 || seen[ key ] { t.Errorf( "%s: %q" , purpose , key ) }
		seen[ key ] = true
	}
	if SubKey( "not hex" , PurposeDB ) != "" { t.Error( "a bad key gave a subkey" ) }
}

// The same vector as the Python template's test_sealing.py: the two templates
// derive the same subkeys from the same secret.key.
func TestTheSubkeyMatchesTheOtherTemplate( t *testing.T ) {
	key := make( []byte , 32 )
	for index := range key { key[ index ] = byte( index ) }
	if got := SubKey( hex.EncodeToString( key ) , PurposeDB ); got != "5e724771cefe68145ad1684f85845a31bd5bf8cdeb61a78bd6a1bde0656139fd" {
		t.Fatalf( "%s" , got )
	}
}
