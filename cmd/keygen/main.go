package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

func main() {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	fmt.Println("PUBLIC_KEY=" + base64.StdEncoding.EncodeToString(pub))
	fmt.Println("PRIVATE_KEY=" + base64.StdEncoding.EncodeToString(priv))
}