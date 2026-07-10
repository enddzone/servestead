package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

var savedPangolinInitialSetupComplete = func(ctx context.Context, dashboardURL string) (bool, error) {
	return pangolinInitialSetupComplete(ctx, pangolinRegistrationHTTPClient, dashboardURL)
}

func runPangolinCredentials(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("pangolin-credentials", flag.ContinueOnError)
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
	return printSavedPangolinCredentials(store, *profileID, *ip, stdout)
}

func printSavedPangolinCredentials(store ProfileStore, profileID, ip string, output io.Writer) error {
	if profileID == "" {
		resolved, err := resolvePangolinCredentialProfileID(store, ip)
		if err != nil {
			return err
		}
		profileID = resolved
	}

	profile, _, err := store.Load(profileID)
	if err != nil {
		return err
	}
	secrets, err := store.LoadSecrets(profileID)
	if err != nil {
		return err
	}
	if profile.BaseDomain != "" && secrets.PangolinSetupToken != "" {
		complete, err := savedPangolinInitialSetupComplete(context.Background(), "https://pangolin."+profile.BaseDomain)
		if err == nil && !complete {
			printPangolinInitialSetupAccess(output, profile.BaseDomain, secrets.PangolinSetupToken)
			return nil
		}
	}
	if secrets.PangolinAdminPassword == "" {
		return errors.New("the saved profile does not have Pangolin administrator credentials; run its proxy stage")
	}
	printPangolinAdminCredentials(output, profile, secrets)
	return nil
}

func resolvePangolinCredentialProfileID(store ProfileStore, ip string) (string, error) {
	matches, err := store.ResolveByIP(ip)
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no saved profile found for %s", ip)
	case 1:
		return matches[0].ID, nil
	default:
		ids := make([]string, len(matches))
		for i, match := range matches {
			ids[i] = match.ID
		}
		return "", fmt.Errorf("multiple saved profiles found for %s; rerun with --profile and one of: %s", ip, strings.Join(ids, ", "))
	}
}

func printPangolinAdminCredentials(output io.Writer, profile Profile, secrets ProfileSecrets) {
	fmt.Fprintf(output, "Pangolin URL: https://pangolin.%s\n", profile.BaseDomain)
	fmt.Fprintf(output, "Username: %s\n", firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail))
	fmt.Fprintf(output, "Password: %s\n", secrets.PangolinAdminPassword)
}

func printPangolinInitialSetupAccess(output io.Writer, baseDomain, setupToken string) {
	fmt.Fprintf(output, "Pangolin initial setup: https://pangolin.%s/auth/initial-setup\n", baseDomain)
	fmt.Fprintf(output, "Setup token: %s\n", setupToken)
}
