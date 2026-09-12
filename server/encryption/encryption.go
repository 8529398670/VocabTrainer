// Package encryption is the one place this project does cryptography.
//
// Nothing else in the server should import crypto/rand, bcrypt, chacha, or
// any other primitive directly -- routing through here means there is a
// single file to audit when you want to answer "how does this app handle
// secrets", and a single file to change if a primitive ever needs swapping.
//
// Two deliberate differences from the reference implementation this API
// mirrors:
//
//  1. Random bytes come straight from crypto/rand. A hand-rolled PRNG on top
//     of a CSPRNG cannot be stronger than the CSPRNG, only harder to review,
//     and every value here (session secrets, login secrets) is one an
//     attacker would love to predict.
//  2. Anything on an authentication path returns an error instead of
//     swallowing it. A decrypt that silently returns "" turns a tampered
//     cookie into an empty-but-successful value, which is exactly the kind
//     of failure that becomes an auth bypass.
package encryption

import (
	base64 "encoding/base64"
	binary "encoding/binary"
	hex "encoding/hex"
	errors "errors"

	random "crypto/rand"
	sha256 "crypto/sha256"
	subtle "crypto/subtle"

	kyberk2so "github.com/symbolicsoft/kyber-k2so"
	bcrypt "golang.org/x/crypto/bcrypt"
	chacha "golang.org/x/crypto/chacha20poly1305"
	curve25519 "golang.org/x/crypto/curve25519"
	secretbox "golang.org/x/crypto/nacl/secretbox"
)

var (
	ErrBadKey     = errors.New( "encryption: key must be 32 bytes hex-encoded" )
	ErrCipherText = errors.New( "encryption: cipher text is malformed or was tampered with" )
)

// ---------------------------------------------------------------------------
// randomness
// ---------------------------------------------------------------------------

// GenerateRandomBytes returns n cryptographically secure random bytes. It
// panics rather than returning short/predictable output, because every caller
// here is minting a credential -- continuing with weak randomness would be
// far worse than crashing at startup.
func GenerateRandomBytes( byte_length int ) ( result []byte ) {
	result = make( []byte , byte_length )
	if _ , err := random.Read( result ); err != nil {
		panic( "encryption: system CSPRNG unavailable: " + err.Error() )
	}
	return
}

// GenerateRandomString returns a hex string of the requested length.
func GenerateRandomString( string_length int ) ( result string ) {
	byte_length := ( ( string_length + 1 ) / 2 )
	result = hex.EncodeToString( GenerateRandomBytes( byte_length ) )[ :string_length ]
	return
}

// GenerateURLToken returns a URL-safe random token. Used for login suffixes
// and session secrets, so it must stay free of characters that would need
// escaping in a path segment or a cookie value.
func GenerateURLToken( byte_length int ) ( result string ) {
	result = base64.RawURLEncoding.EncodeToString( GenerateRandomBytes( byte_length ) )
	return
}

func Sha256Sum( entries [][]byte ) ( result []byte ) {
	hasher := sha256.New()
	for _ , entry := range entries {
		hasher.Write( entry )
	}
	result = hasher.Sum( nil )
	return
}

// Sha256Hex is the lookup-key hash for high-entropy secrets (session
// secrets). bcrypt is the wrong tool there: it is deliberately slow, and it
// would run on every single authenticated request. Against a 256-bit random
// secret there is nothing to brute force, so a fast hash is the right call.
func Sha256Hex( value string ) ( result string ) {
	result = hex.EncodeToString( Sha256Sum( [][]byte{ []byte( value ) } ) )
	return
}

// ConstantTimeEqual compares two hex/ASCII digests without leaking, through
// how long the comparison takes, how many leading characters matched. Go's ==
// on strings returns early at the first difference, which is exactly the
// signal a timing attack feeds on.
func ConstantTimeEqual( a string , b string ) ( result bool ) {
	result = subtle.ConstantTimeCompare( []byte( a ) , []byte( b ) ) == 1
	return
}

// ---------------------------------------------------------------------------
// bcrypt -- for one-time login secrets
// ---------------------------------------------------------------------------

// BcryptHash is used on login-link secrets, which are redeemed at most once.
// The cost is paid on a single redemption rather than on every request, so
// bcrypt's slowness buys real protection here without hurting throughput.
func BcryptHash( plain_text string ) ( result string , err error ) {
	hashed , err := bcrypt.GenerateFromPassword( []byte( plain_text ) , bcrypt.DefaultCost )
	if err != nil { return }
	result = string( hashed )
	return
}

func BcryptCompare( hashed string , plain_text string ) ( ok bool ) {
	ok = bcrypt.CompareHashAndPassword( []byte( hashed ) , []byte( plain_text ) ) == nil
	return
}

// ---------------------------------------------------------------------------
// keys
// ---------------------------------------------------------------------------

func GenerateKeyHex() ( key_hex string ) {
	key_hex = hex.EncodeToString( GenerateRandomBytes( 32 ) )
	return
}

func decodeKey( key_hex string ) ( key [32]byte , err error ) {
	raw , decode_err := hex.DecodeString( key_hex )
	if decode_err != nil || len( raw ) != 32 {
		err = ErrBadKey
		return
	}
	copy( key[ : ] , raw )
	return
}

// ---------------------------------------------------------------------------
// secretbox
// ---------------------------------------------------------------------------

func SecretBoxGenerateRandomKey() ( key [32]byte ) {
	copy( key[ : ] , GenerateRandomBytes( 32 ) )
	return
}

func SecretBoxEncrypt( key_hex string , plain_text string ) ( result string , err error ) {
	key , err := decodeKey( key_hex )
	if err != nil { return }
	var nonce [24]byte
	copy( nonce[ : ] , GenerateRandomBytes( 24 ) )
	sealed := secretbox.Seal( nonce[ : ] , []byte( plain_text ) , &nonce , &key )
	result = base64.RawURLEncoding.EncodeToString( sealed )
	return
}

func SecretBoxDecrypt( key_hex string , encrypted string ) ( result string , err error ) {
	key , err := decodeKey( key_hex )
	if err != nil { return }
	raw , decode_err := base64.RawURLEncoding.DecodeString( encrypted )
	if decode_err != nil || len( raw ) < 24 {
		err = ErrCipherText
		return
	}
	var nonce [24]byte
	copy( nonce[ : ] , raw[ 0:24 ] )
	opened , ok := secretbox.Open( nil , raw[ 24: ] , &nonce , &key )
	if ok == false {
		err = ErrCipherText
		return
	}
	result = string( opened )
	return
}

// ---------------------------------------------------------------------------
// chacha20poly1305 -- cookie payloads and values at rest
// ---------------------------------------------------------------------------

// ChaChaEncryptBytes uses the extended-nonce variant so that a 24-byte random
// nonce can be generated per message without any risk of collision. The
// nonce is prepended to the cipher text; the Poly1305 tag means a modified
// value fails to open rather than decrypting to garbage.
func ChaChaEncryptBytes( key_hex string , plain_text []byte ) ( result []byte , err error ) {
	key , err := decodeKey( key_hex )
	if err != nil { return }
	aead , aead_err := chacha.NewX( key[ : ] )
	if aead_err != nil {
		err = aead_err
		return
	}
	nonce := GenerateRandomBytes( aead.NonceSize() )
	result = aead.Seal( nonce , nonce , plain_text , nil )
	return
}

func ChaChaDecryptBytes( key_hex string , encrypted []byte ) ( result []byte , err error ) {
	key , err := decodeKey( key_hex )
	if err != nil { return }
	aead , aead_err := chacha.NewX( key[ : ] )
	if aead_err != nil {
		err = aead_err
		return
	}
	if len( encrypted ) < aead.NonceSize() {
		err = ErrCipherText
		return
	}
	nonce := encrypted[ :aead.NonceSize() ]
	opened , open_err := aead.Open( nil , nonce , encrypted[ aead.NonceSize(): ] , nil )
	if open_err != nil {
		err = ErrCipherText
		return
	}
	result = opened
	return
}

func ChaChaEncryptString( key_hex string , plain_text string ) ( result string , err error ) {
	sealed , err := ChaChaEncryptBytes( key_hex , []byte( plain_text ) )
	if err != nil { return }
	result = base64.RawURLEncoding.EncodeToString( sealed )
	return
}

func ChaChaDecryptBase64String( key_hex string , encrypted string ) ( result string , err error ) {
	raw , decode_err := base64.RawURLEncoding.DecodeString( encrypted )
	if decode_err != nil {
		err = ErrCipherText
		return
	}
	opened , err := ChaChaDecryptBytes( key_hex , raw )
	if err != nil { return }
	result = string( opened )
	return
}

// ---------------------------------------------------------------------------
// curve25519 + kyber
//
// Neither is wired into the login flow -- session cookies are opaque random
// values checked against the database, which needs no key exchange at all.
// They are here because apps built on this template sometimes need to hand a
// browser or a peer service a public key (end-to-end encrypted messages,
// signed payloads between two instances). Kyber alongside X25519 gives the
// usual hybrid: even an attacker recording traffic today for a future
// quantum computer has to break both.
// ---------------------------------------------------------------------------

func CurveX25519GenerateKeyPair() ( public_key [32]byte , private_key [32]byte , err error ) {
	copy( private_key[ : ] , GenerateRandomBytes( 32 ) )
	public_key_bytes , err := curve25519.X25519( private_key[ : ] , curve25519.Basepoint )
	if err != nil { return }
	copy( public_key[ : ] , public_key_bytes )
	return
}

func CurveX25519KeyExchange( private_key [32]byte , other_public_key [32]byte ) ( shared_secret [32]byte , err error ) {
	shared_secret_bytes , err := curve25519.X25519( private_key[ : ] , other_public_key[ : ] )
	if err != nil { return }
	copy( shared_secret[ : ] , shared_secret_bytes )
	return
}

func ChaChaSharedSecretEncryptMessage( shared_secret [32]byte , message []byte ) ( result []byte , err error ) {
	result , err = ChaChaEncryptBytes( hex.EncodeToString( shared_secret[ : ] ) , message )
	return
}

func ChaChaSharedSecretDecryptMessage( shared_secret [32]byte , encrypted []byte ) ( result []byte , err error ) {
	result , err = ChaChaDecryptBytes( hex.EncodeToString( shared_secret[ : ] ) , encrypted )
	return
}

func KyberGenerateKeyPair() ( public_key [1568]byte , private_key [3168]byte , err error ) {
	private_key , public_key , err = kyberk2so.KemKeypair1024()
	return
}

func KyberEncrypt( public_key [1568]byte ) ( cipher_text [1568]byte , shared_secret [32]byte , err error ) {
	cipher_text , shared_secret , err = kyberk2so.KemEncrypt1024( public_key )
	return
}

func KyberDecrypt( cipher_text [1568]byte , private_key [3168]byte ) ( shared_secret [32]byte , err error ) {
	shared_secret , err = kyberk2so.KemDecrypt1024( cipher_text , private_key )
	return
}

// Uint64ToBytes / BytesToUint64 keep bolt's big-endian integer keys sorting
// correctly, which is what makes cursor iteration return records in
// insertion order.
func Uint64ToBytes( value uint64 ) ( result []byte ) {
	result = make( []byte , 8 )
	binary.BigEndian.PutUint64( result , value )
	return
}

func BytesToUint64( raw []byte ) ( result uint64 ) {
	if len( raw ) != 8 { return }
	result = binary.BigEndian.Uint64( raw )
	return
}
