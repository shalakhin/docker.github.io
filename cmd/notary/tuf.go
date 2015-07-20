package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/pkg/term"
	notaryclient "github.com/docker/notary/client"
	"github.com/docker/notary/trustmanager"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// FIXME: This should not be hardcoded
const hardcodedBaseURL = "https://notary-server:4443"

var retriever trustmanager.PassphraseRetriever

func init() {
	retriever = getNotaryPassphraseRetriever()
}

var remoteTrustServer string

var cmdTufList = &cobra.Command{
	Use:   "list [ GUN ]",
	Short: "Lists targets for a trusted collection.",
	Long:  "Lists all targets for a trusted collection identified by the Globally Unique Name.",
	Run:   tufList,
}

var cmdTufAdd = &cobra.Command{
	Use:   "add [ GUN ] <target> <file>",
	Short: "adds the file as a target to the trusted collection.",
	Long:  "adds the file as a target to the local trusted collection identified by the Globally Unique Name.",
	Run:   tufAdd,
}

var cmdTufRemove = &cobra.Command{
	Use:   "remove [ GUN ] <target>",
	Short: "Removes a target from a trusted collection.",
	Long:  "removes a target from the local trusted collection identified by the Globally Unique Name.",
	Run:   tufRemove,
}

var cmdTufInit = &cobra.Command{
	Use:   "init [ GUN ]",
	Short: "initializes a local trusted collection.",
	Long:  "initializes a local trusted collection identified by the Globally Unique Name.",
	Run:   tufInit,
}

var cmdTufLookup = &cobra.Command{
	Use:   "lookup [ GUN ] <target>",
	Short: "Looks up a specific target in a trusted collection.",
	Long:  "looks up a specific target in a trusted collection identified by the Globally Unique Name.",
	Run:   tufLookup,
}

var cmdTufPublish = &cobra.Command{
	Use:   "publish [ GUN ]",
	Short: "publishes the local trusted collection.",
	Long:  "publishes the local trusted collection identified by the Globally Unique Name, sending the local changes to a remote trusted server.",
	Run:   tufPublish,
}

var cmdVerify = &cobra.Command{
	Use:   "verify [ GUN ] <target>",
	Short: "verifies if the content is included in the trusted collection",
	Long:  "verifies if the data passed in STDIN is included in the trusted collection identified by the Global Unique Name.",
	Run:   verify,
}

func tufAdd(cmd *cobra.Command, args []string) {
	if len(args) < 3 {
		cmd.Usage()
		fatalf("must specify a GUN, target, and path to target data")
	}

	gun := args[0]
	targetName := args[1]
	targetPath := args[2]

	repo, err := notaryclient.NewNotaryRepository(viper.GetString("baseTrustDir"), gun, hardcodedBaseURL,
		getInsecureTransport(), retriever)
	if err != nil {
		fatalf(err.Error())
	}

	target, err := notaryclient.NewTarget(targetName, targetPath)
	if err != nil {
		fatalf(err.Error())
	}
	err = repo.AddTarget(target)
	if err != nil {
		fatalf(err.Error())
	}
	fmt.Println("Successfully added targets")
}

func tufInit(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		cmd.Usage()
		fatalf("Must specify a GUN")
	}

	gun := args[0]

	nRepo, err := notaryclient.NewNotaryRepository(viper.GetString("baseTrustDir"), gun, hardcodedBaseURL,
		getInsecureTransport(), retriever)
	if err != nil {
		fatalf(err.Error())
	}

	keysList := nRepo.KeyStoreManager.RootKeyStore().ListKeys()
	var rootKeyID string
	if len(keysList) < 1 {
		fmt.Println("No root keys found. Generating a new root key...")
		rootKeyID, err = nRepo.KeyStoreManager.GenRootKey("ECDSA")
		if err != nil {
			fatalf(err.Error())
		}
	} else {
		rootKeyID = keysList[0]
		fmt.Println("Root key found.")
	}

	rootCryptoService, err := nRepo.KeyStoreManager.GetRootCryptoService(rootKeyID)
	if err != nil {
		fatalf(err.Error())
	}

	err = nRepo.Initialize(rootCryptoService)
	if err != nil {
		fatalf(err.Error())
	}
}

func tufList(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		cmd.Usage()
		fatalf("must specify a GUN")
	}
	gun := args[0]
	repo, err := notaryclient.NewNotaryRepository(viper.GetString("baseTrustDir"), gun, hardcodedBaseURL,
		getInsecureTransport(), retriever)
	if err != nil {
		fatalf(err.Error())
	}

	// Retreive the remote list of signed targets
	targetList, err := repo.ListTargets()
	if err != nil {
		fatalf(err.Error())
	}

	// Print all the available targets
	for _, t := range targetList {
		fmt.Printf("%s %x %d\n", t.Name, t.Hashes["sha256"], t.Length)
	}
}

func tufLookup(cmd *cobra.Command, args []string) {
	if len(args) < 2 {
		cmd.Usage()
		fatalf("must specify a GUN and target")
	}
	gun := args[0]
	targetName := args[1]

	repo, err := notaryclient.NewNotaryRepository(viper.GetString("baseTrustDir"), gun, hardcodedBaseURL,
		getInsecureTransport(), retriever)
	if err != nil {
		fatalf(err.Error())
	}

	// TODO(diogo): Parse Targets and print them
	target, err := repo.GetTargetByName(targetName)
	if err != nil {
		fatalf(err.Error())
	}

	fmt.Println(target.Name, fmt.Sprintf("sha256:%x", target.Hashes["sha256"]), target.Length)
}

func tufPublish(cmd *cobra.Command, args []string) {
	if len(args) < 1 {
		cmd.Usage()
		fatalf("Must specify a GUN")
	}

	gun := args[0]

	fmt.Println("Pushing changes to ", gun, ".")

	repo, err := notaryclient.NewNotaryRepository(viper.GetString("baseTrustDir"), gun, hardcodedBaseURL,
		getInsecureTransport(), retriever)
	if err != nil {
		fatalf(err.Error())
	}

	err = repo.Publish()
	if err != nil {
		fatalf(err.Error())
	}
}

func tufRemove(cmd *cobra.Command, args []string) {
	if len(args) < 2 {
		cmd.Usage()
		fatalf("must specify a GUN and target")
	}
	gun := args[0]
	targetName := args[1]

	//c := changelist.NewTufChange(changelist.ActionDelete, "targets", "target", targetName, nil)
	//err := cl.Add(c)
	//if err != nil {
	//	fatalf(err.Error())
	//}

	// TODO(diogo): Implement RemoveTargets in libnotary
	fmt.Println("Removing target ", targetName, " from ", gun)
}

func verify(cmd *cobra.Command, args []string) {
	if len(args) < 2 {
		cmd.Usage()
		fatalf("must specify a GUN and target")
	}

	// Reads all of the data on STDIN
	//TODO (diogo): Change this to do a streaming hash
	payload, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		fatalf("error reading content from STDIN: %v", err)
	}

	//TODO (diogo): This code is copy/pasted from lookup.
	gun := args[0]
	targetName := args[1]
	repo, err := notaryclient.NewNotaryRepository(viper.GetString("baseTrustDir"), gun, hardcodedBaseURL,
		getInsecureTransport(), retriever)
	if err != nil {
		fatalf(err.Error())
	}

	// TODO(diogo): Parse Targets and print them
	target, err := repo.GetTargetByName(targetName)
	if err != nil {
		logrus.Error("notary: data not present in the trusted collection.")
		os.Exit(-11)
	}

	// Create hasher and hash data
	stdinHash := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	serverHash := fmt.Sprintf("sha256:%s", target.Hashes["sha256"])
	if stdinHash != serverHash {
		logrus.Error("notary: data not present in the trusted collection.")
		os.Exit(1)
	} else {
		_, _ = os.Stdout.Write(payload)
	}
	return
}

func getNotaryPassphraseRetriever() trustmanager.PassphraseRetriever {
	userEnteredTargetsSnapshotsPass := false
	targetsSnapshotsPass := ""

	return func(keyID string, alias string, createNew bool, numAttempts int) (string, bool, error) {
		if numAttempts == 0 && userEnteredTargetsSnapshotsPass && (alias == "snapshot" || alias == "targets") {
			fmt.Println("return cached value")

			return targetsSnapshotsPass, false, nil
		}
		if numAttempts > 3 && !createNew {
			return "", true, errors.New("Too many attempts")
		}

		state, err := term.SaveState(0)
		if err != nil {
			return "", false, err
		}
		term.DisableEcho(0, state)
		defer term.RestoreTerminal(0, state)

		stdin := bufio.NewReader(os.Stdin)

		if createNew {
			fmt.Printf("Enter passphrase for new %s key with id %s: ", alias, keyID)
		} else {
			fmt.Printf("Enter key passphrase for %s key with id %s: ", alias, keyID)
		}

		passphrase, err := stdin.ReadBytes('\n')
		fmt.Println()
		if err != nil {
			return "", false, err
		}
		passphrase = passphrase[0 : len(passphrase)-1]

		if !createNew {
			retPass := string(passphrase)
			if alias == "snapshot" || alias == "targets" {
				userEnteredTargetsSnapshotsPass = true
				targetsSnapshotsPass = retPass
			}
			return string(passphrase), false, nil
		}

		if len(passphrase) < 8 {
			fmt.Println("Please use a password manager to generate and store a good random passphrase.")
			return "", false, errors.New("Passphrase too short")
		}

		fmt.Printf("Repeat passphrase for new %s key with id %s: ", alias, keyID)
		confirmation, err := stdin.ReadBytes('\n')
		fmt.Println()
		if err != nil {
			return "", false, err
		}
		confirmation = confirmation[0 : len(confirmation)-1]

		if !bytes.Equal(passphrase, confirmation) {
			return "", false, errors.New("The entered passphrases do not match")
		}
		retPass := string(passphrase)

		if alias == "snapshots" || alias == "targets" {
			userEnteredTargetsSnapshotsPass = true
			targetsSnapshotsPass = retPass
		}

		return retPass, false, nil
	}
}

func getInsecureTransport() *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
}
