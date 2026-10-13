// keygen creates an API key and stores its hash in the keys file.
//
//	go run ./cmd/keygen -plan free -owner "me"
package main

import (
	"flag"
	"fmt"
	"os"

	"limitlab/internal/apikey"
	"limitlab/internal/keys"
	"limitlab/internal/plans"
)

func main() {
	plan := flag.String("plan", "free", "plan for the key (free or pro)")
	owner := flag.String("owner", "local testing", "who the key is for")
	file := flag.String("file", envOr("KEYS_FILE", "keys.local.json"), "keys file")
	flag.Parse()

	if _, ok := plans.Defaults()[*plan]; !ok {
		fail("unknown plan %q", *plan)
	}

	raw, err := apikey.Generate()
	if err != nil {
		fail("generating key: %v", err)
	}
	k, err := keys.Append(*file, keys.Key{
		Hash:   apikey.Hash(raw),
		Prefix: apikey.Display(raw),
		Owner:  *owner,
		Plan:   *plan,
	})
	if err != nil {
		fail("saving key: %v", err)
	}

	fmt.Printf("key #%d (%s plan) saved to %s\n\n", k.ID, k.Plan, *file)
	fmt.Println(raw)
	fmt.Println("\nOnly the hash is stored, so copy the key now. Restart the api to pick it up.")
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "keygen: "+format+"\n", args...)
	os.Exit(1)
}
