package models

import (
	json "encoding/json"
	fmt "fmt"

	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
)

// rekeyFields is every field sealed on its own, by bucket, with the purpose
// whose subkey of secret.key seals it (see encryption.SubKey).
var rekeyFields = map[string]map[string]string{
}

// db.Open brings a database sealed the old way up to date with this.
func init() {
	db.FieldRekey = RekeyRecord
}

// RekeyRecord is what moving the database to a new key has to do beyond the
// record's own encryption: a TOTP secret is encrypted again on its own (see
// two_factor.go), so it has to be opened with the old key and sealed
// with the new one. Anything else that encrypts a field with the secret key
// belongs here too, or that field is lost at the next rotation.
//
// The record is edited as raw JSON so that fields this code never touches
// come back exactly as they went in.
func RekeyRecord( old_key string , new_key string ) ( rewrite func( bucket string , plain []byte ) ( []byte , error ) ) {
	rewrite = func( bucket string , plain []byte ) ( result []byte , err error ) {
		result = plain
		fields := rekeyFields[ bucket ]
		if len( fields ) == 0 { return }
		record := map[string]json.RawMessage{}
		if err = json.Unmarshal( plain , &record ); err != nil { return }
		changed := false
		for field , purpose := range fields {
			raw , present := record[ field ]
			if present == false { continue }
			var sealed string
			if err = json.Unmarshal( raw , &sealed ); err != nil { return }
			if sealed == "" { continue }
			// A field sealed the old way, with secret.key itself, opens with
			// that: this is also how an old database is brought up to date.
			secret , open_err := encryption.ChaChaDecryptBase64String( encryption.SubKey( old_key , purpose ) , sealed )
			if open_err != nil {
				secret , open_err = encryption.ChaChaDecryptBase64String( old_key , sealed )
			}
			if open_err != nil {
				err = fmt.Errorf( "%s: %w" , field , open_err )
				return
			}
			if sealed , err = encryption.ChaChaEncryptString( encryption.SubKey( new_key , purpose ) , secret ); err != nil { return }
			if record[ field ] , err = json.Marshal( sealed ); err != nil { return }
			changed = true
		}
		if changed {
			result , err = json.Marshal( record )
		}
		return
	}
	return
}
