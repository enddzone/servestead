package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

func runPangolinToken(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("pangolin-token", flag.ContinueOnError)
	flags.SetOutput(stderr)
	profileID := flags.String("profile", "", "saved profile ID")
	ip := flags.String("ip", "", "server IPv4 address or hostname")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if (*profileID == "") == (*ip == "") {
		return errors.New("exactly one of --profile or --ip is required")
	}

	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	return printSavedPangolinToken(store, *profileID, *ip, stdout)
}

func printSavedPangolinToken(store ProfileStore, profileID, ip string, output io.Writer) error {
	if profileID == "" {
		matches, err := store.ResolveByIP(ip)
		if err != nil {
			return err
		}
		switch len(matches) {
		case 0:
			return fmt.Errorf("no saved profile found for %s", ip)
		case 1:
			profileID = matches[0].ID
		default:
			ids := make([]string, len(matches))
			for i, match := range matches {
				ids[i] = match.ID
			}
			return fmt.Errorf("multiple saved profiles found for %s; rerun with --profile and one of: %s", ip, strings.Join(ids, ", "))
		}
	}

	profile, _, err := store.Load(profileID)
	if err != nil {
		return err
	}
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		return err
	}
	if secrets.PangolinSetupToken == "" {
		return errors.New("the saved profile does not have a Pangolin setup token; run its proxy stage to generate and deploy one")
	}
	printPangolinSetupGuidance(output, profile.BaseDomain, secrets.PangolinSetupToken)
	return nil
}
