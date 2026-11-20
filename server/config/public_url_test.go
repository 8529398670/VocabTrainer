package config

import (
	testing "testing"
)

// F11: scripts\run.bat and login.bat print server.public_url, and cmd runs
// whatever follows a & | < > ^ % or " in it. Only a plain origin passes.
func TestPublicURLIsAPlainOrigin( t *testing.T ) {
	for _ , value := range []string{
		"https://a&calc&b" , "https://a|b" , `https://a"b` , "https://a%b" , "https://a^b" ,
		"https://a b" , "https://a<b" , "https://a>b" , "https://a!b" , "https://(a)" ,
		"ftp://a.example.com" , "https://a.example.com/path" , "https://a.example.com:port" ,
	} {
		if validPublicURL( value ) { t.Errorf( "%q was accepted" , value ) }
	}
	for _ , value := range []string{
		"" , "https://app.example.com" , "https://app.example.com:8443/" , "http://[::1]:8080" ,
		"http://127.0.0.1:3000" , "http://localhost:8080" ,
	} {
		if validPublicURL( value ) == false { t.Errorf( "%q was refused" , value ) }
	}
}

// S13: server.trusted_proxies refuses any range that trusts far more than a
// proxy, not just the strings 0.0.0.0/0 and ::/0. Each of these trusts every
// address, or every container.
func TestATrustedProxyRangeIsNarrow( t *testing.T ) {
	for _ , value := range []string{
		"0.0.0.0/0" , "::/0" , "1.2.3.4/0" , "::1/0" , "0.0.0.0/1" , "128.0.0.0/1" , "10.0.0.0/7" ,
		"2001::/15" , "::ffff:0.0.0.0/96" , "::ffff:10.0.0.0/100" ,
	} {
		if narrowEnoughProxy( value ) { t.Errorf( "%q was accepted" , value ) }
	}
	for _ , value := range []string{
		"127.0.0.1" , "::1" , "172.17.0.1" , "10.0.0.0/8" , "173.245.48.0/20" , "104.16.0.0/13" ,
		"2400:cb00::/32" , "2001::/16" , "::ffff:10.0.0.0/104" ,
	} {
		if narrowEnoughProxy( value ) == false { t.Errorf( "%q was refused" , value ) }
	}
}
