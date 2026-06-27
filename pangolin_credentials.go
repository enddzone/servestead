package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

func runPangolinCredentials(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("pangolin-credentials", flag.ContinueOnError)
	flags.SetOutput(stderr)
	profileID := flags.String("profile", "", "saved profile ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *profileID == "" {
		return errors.New("--profile is required")
	}
	store, err := newDefaultProfileStore()
	if err != nil {
		return err
	}
	profile, _, err := store.Load(*profileID)
	if err != nil {
		return err
	}
	secrets, err := store.LoadSecrets(*profileID)
	if err != nil {
		return err
	}
	if secrets.PangolinAdminPassword == "" {
		return errors.New("the saved profile does not have Pangolin administrator credentials; run its proxy stage")
	}
	fmt.Fprintf(stdout, "Pangolin URL: https://pangolin.%s\n", profile.BaseDomain)
	fmt.Fprintf(stdout, "Email: %s\n", firstNonEmpty(profile.PangolinAdminEmail, profile.LetsEncryptEmail))
	fmt.Fprintf(stdout, "Password: %s\n", secrets.PangolinAdminPassword)
	return nil
}
