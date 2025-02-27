package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/forestvpn/cli/actions"
	"github.com/forestvpn/cli/auth"
	"github.com/forestvpn/cli/timezone"
	"github.com/forestvpn/cli/utils"
	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/getsentry/sentry-go"
	"github.com/urfave/cli/v2"
)

// appVersion value is stored in an environment variable and assigned during the build with ldflags.
var appVersion string

// DSN is a Data Source Name for Sentry. It is stored in an environment variable and assigned during the build with ldflags.
//
// See https://docs.sentry.io/product/sentry-basics/dsn-explainer/ for more information.
const Dsn = "https://ef875c1346ed49289812f9df5a44f03f@sentry.fvpn.uk/8"

const url = "https://forestvpn.com/checkout/"

func main() {
	// email is user's email address used to sign in or sign up on the Firebase.
	var email string
	// country is stores prompted country name to filter locations by country.
	var country string

	err := auth.Init()

	if err != nil {
		sentry.CaptureException(err)
		log.Fatal(err)
		os.Exit(1)
	}

	err = sentry.Init(sentry.ClientOptions{
		Dsn: Dsn,
	})

	if err != nil {
		log.Fatalf("sentry.Init: %s", err)
		os.Exit(1)
	}

	defer sentry.Flush(2 * time.Second)

	cli.VersionPrinter = func(cCtx *cli.Context) {
		fmt.Println(cCtx.App.Version)
	}

	app := &cli.App{
		Version:              appVersion,
		EnableBashCompletion: true,
		Suggest:              true,
		Name:                 "fvpn",
		Usage:                "fast, secure, and modern VPN",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:        "verbose",
				Aliases:     []string{"V"},
				Usage:       "make commands more talkative",
				Value:       false,
				Destination: &utils.Verbose,
			},
		},
		Commands: []*cli.Command{
			{
				Name:  "account",
				Usage: "manage ForestVPN accounts",
				Subcommands: []*cli.Command{
					{
						Name:  "ls",
						Usage: "see local accounts ever logged in",
						Action: func(c *cli.Context) error {
							return auth.PrintLocalAccounts()
						},
					},
					{
						Name:  "status",
						Usage: "see logged-in account info",
						Action: func(c *cli.Context) error {
							profile := auth.OpenUserDB().CurrentUser()
							if err = profile.SignIn(utils.ApiHost); err != nil {
								return err
							}

							authClientWrapper, err := actions.GetAuthClientWrapper(profile, utils.ApiHost)
							if err != nil {
								return err
							}

							b, err := authClientWrapper.GetUnexpiredOrMostRecentBillingFeature(profile.ID)
							if err != nil {
								return err
							}

							expiryDate := b.GetExpiryDate()
							now := time.Now()
							left := expiryDate.Sub(now)
							caser := cases.Title(language.English)
							plan := caser.String(strings.Split(b.GetBundleId(), ".")[2])
							fmt.Printf("Logged-in as %s\n", profile.Email)
							fmt.Printf("Plan: %s\n", plan)
							tz, err := utils.GetLocalTimezone()

							if err != nil {
								sentry.CaptureException(err)
								_, offset := now.Zone()

								tz = timezone.GetGmtTimezone(offset)
							}

							if now.After(expiryDate) {
								t := now.Sub(expiryDate)
								fmt.Printf("Status: expired %s ago at %s %s\n", utils.HumanizeDuration(t), expiryDate.Format("2006-01-02 15:04:05"), tz)
							} else {
								fmt.Printf("Status: expires in %s at %s %s\n", utils.HumanizeDuration(left), expiryDate.Format("2006-01-02 15:04:05"), tz)

							}

							return nil
						},
					},
					{
						Name:  "login",
						Usage: "log into your ForestVPN account",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:        "email",
								Destination: &email,
								Usage:       "your email address",
								Value:       "",
								Aliases:     []string{"e"},
							},
						},
						Action: func(c *cli.Context) error {
							profile := auth.OpenUserDB().CreateUser()
							if err = profile.SignIn(utils.ApiHost); err != nil {
								return err
							}

							if err == nil {
								fmt.Println("Logged in")
							}

							return err
						},
					},
					{
						Name:  "logout",
						Usage: "unlink this device from your ForstVPN account",
						Action: func(c *cli.Context) error {
							profile := auth.OpenUserDB().CurrentUser()
							if err = profile.SignIn(utils.ApiHost); err != nil {
								return err
							}

							state := actions.State{WiregaurdInterface: "fvpn0"}
							status := state.GetStatus()
							if status {
								fmt.Println("Please, set down the connection before attempting to log out.")
								fmt.Println("Try 'forest state down'")
								return nil
							}

							profile.MarkAsInactive()
							fmt.Println("Logged out")
							return nil
						},
					},
				},
			},
			{
				Name:  "state",
				Usage: "control the state of the ForestVPN connection",
				Subcommands: []*cli.Command{
					{

						Name:  "up",
						Usage: "connect to the ForestVPN location",
						Flags: []cli.Flag{
							&cli.BoolFlag{
								Name:    "persist",
								Usage:   "Persist VPN connnection through reboots",
								Value:   false,
								Aliases: []string{"p"},
							},
						},
						Action: func(c *cli.Context) error {
							profile := auth.OpenUserDB().CurrentUser()
							if err = profile.SignIn(utils.ApiHost); err != nil {
								return err
							}
							state := actions.State{WiregaurdInterface: "fvpn0"}
							if state.GetStatus() {
								fmt.Println("State is already up and running")
								os.Exit(1)
							}

							client, err := actions.GetAuthClientWrapper(profile, utils.ApiHost)
							if err != nil {
								return err
							}

							b, err := client.GetUnexpiredOrMostRecentBillingFeature(profile.ID)
							if err != nil {
								return err
							}

							device, err := auth.LoadDevice(profile.ID)
							if err != nil {
								return err
							}

							bid := b.GetBundleId()
							location := device.GetLocation()
							now := time.Now()
							exp := b.GetExpiryDate()
							left := exp.Sub(now)
							days := int64(left.Hours() / 24)

							if now.After(exp) {
								if actions.IsPremiumLocation(location) && bid == "com.forestvpn.premium" {
									fmt.Println("The location you were using is now unavailable, as your paid subscription has ended.")
									fmt.Printf("You can keep using ForestVPN once you watch an ad in our mobile app, or simply go Premium at %s.\n", url)
									os.Exit(1)
								} else {
									fmt.Println("Your 30-minute session is over.")
									fmt.Printf("You can keep using ForestVPN once you watch an ad in our mobile app, or simply go Premium at %s.\n", url)
									os.Exit(1)
								}
							} else if bid == "com.forestvpn.freemium" && int(left.Minutes()) < 5 {
								fmt.Println("You currently have less than 5 minutes of free trial left.")
							} else if days == 3 && left.Hours() == 0 || days < 3 && bid == "com.forestvpn.premium" {
								fmt.Println("Your premium subscription will end in less than 3 days.")
							}

							persist := c.Bool("persist")
							err = state.SetUp(profile.ID, persist)

							if err != nil {
								return err
							}

							time.Sleep(1 * time.Second)

							if state.GetStatus() {
								country := location.GetCountry()
								fmt.Printf("Connected to %s, %s\n", location.GetName(), country.GetName())
							} else {
								return errors.New("unexpected error: state.status is false after state is up")
							}

							return nil
						},
					},
					{
						Name:        "down",
						Description: "disconnect from the ForestVPN location",
						Action: func(ctx *cli.Context) error {
							profile := auth.OpenUserDB().CurrentUser()
							if err = profile.SignIn(utils.ApiHost); err != nil {
								return err
							}

							state := actions.State{WiregaurdInterface: "fvpn0"}

							if state.GetStatus() {
								err = state.SetDown(profile.ID)

								if err != nil {
									return err
								}

								if runtime.GOOS == "windows" {
									time.Sleep(1 * time.Second)
								}

								if state.GetStatus() {
									return errors.New("unexpected error: state.status is true after state is down")
								}

								fmt.Println("Disconnected")
							} else {
								fmt.Println("State is already down")
								os.Exit(1)
							}

							return nil
						},
					},
					{
						Name:  "status",
						Usage: "see wether connection is active",
						Action: func(ctx *cli.Context) error {
							profile := auth.OpenUserDB().CurrentUser()
							if err = profile.SignIn(utils.ApiHost); err != nil {
								return err
							}

							state := actions.State{WiregaurdInterface: "fvpn0"}

							if state.GetStatus() {
								device, err := auth.LoadDevice(profile.ID)

								if err != nil {
									return err
								}

								location := device.GetLocation()
								country := location.GetCountry()

								fmt.Printf("Connected to %s, %s\n", location.GetName(), country.GetName())
							} else {
								fmt.Println("Disconnected")
							}

							return nil

						},
					},
				},
			},
			{
				Name:  "location",
				Usage: "manage ForestVPN locations",
				Subcommands: []*cli.Command{
					{
						Name:  "status",
						Usage: "see the location is set as default location to connect",
						Action: func(cCtx *cli.Context) error {
							profile := auth.OpenUserDB().CurrentUser()
							if err = profile.SignIn(utils.ApiHost); err != nil {
								return err
							}

							device, err := auth.LoadDevice(profile.ID)

							if err != nil {
								return err
							}

							location := device.GetLocation()
							country := location.GetCountry()
							fmt.Printf("Default location is set to %s, %s\n", location.GetName(), country.GetName())
							return nil
						},
					},
					{
						Name:  "set",
						Usage: "set the default location by specifying `UUID` or `Name`",
						Action: func(cCtx *cli.Context) error {
							profile := auth.OpenUserDB().CurrentUser()
							if err = profile.SignIn(utils.ApiHost); err != nil {
								return err
							}

							state := actions.State{WiregaurdInterface: "fvpn0"}

							if state.GetStatus() {
								fmt.Println("Please, set down the connection before setting a new location.")
								fmt.Println("Try 'fvpn state down'")
								return nil
							}

							arg := cCtx.Args().Get(0)

							if len(arg) < 1 {
								return errors.New("UUID or name required")
							}

							authClientWrapper, err := actions.GetAuthClientWrapper(profile, utils.ApiHost)
							if err != nil {
								return err
							}

							locations, err := authClientWrapper.ApiClient.GetLocations()
							if err != nil {
								return err
							}

							wrappedLocations := actions.GetLocationWrappers(locations)
							var location actions.LocationWrapper
							id, err := uuid.Parse(arg)
							found := false

							if err != nil {
								for _, loc := range wrappedLocations {
									if strings.EqualFold(loc.Location.GetName(), arg) {
										location = loc
										found = true
										break
									}
								}
							} else {
								for _, loc := range wrappedLocations {
									if strings.EqualFold(location.Location.GetId(), id.String()) {
										location = loc
										found = false
										break
									}
								}
							}

							if !found {
								err := fmt.Errorf("no such location: %s", arg)
								return err
							}

							b, err := authClientWrapper.GetUnexpiredOrMostRecentBillingFeature(profile.ID)

							if err != nil {
								return err
							}

							bid := b.GetBundleId()
							expired := time.Now().After(b.GetExpiryDate())

							if location.Premium && bid == "com.forestvpn.freemium" || expired {
								fmt.Printf("The location you want to use is now unavailable, as it requires a paid subscription. You can unlock it by going Premium at %s.\n", url)
								return nil
							}

							device, err := auth.LoadDevice(profile.ID)
							if err != nil {
								return err
							}

							device, err = authClientWrapper.ApiClient.UpdateDevice(device.GetId(), location.Location.GetId())
							if err != nil {
								return err
							}

							err = auth.UpdateProfileDevice(device, profile.ID)
							if err != nil {
								return err
							}

							if !utils.IsOpenWRT() {
								err = authClientWrapper.SetLocation(device, profile.ID)

								if err != nil {
									return err
								}
							}

							country := location.Location.GetCountry()
							fmt.Printf("Default location is set to %s, %s\n", location.Location.GetName(), country.GetName())
							return nil
						},
					},
					{
						Name:  "ls",
						Usage: "show available ForestVPN locations",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:        "country",
								Destination: &country,
								Usage:       "show locations by specific country",
								Value:       "",
								Aliases:     []string{"c"},
								Required:    false,
							},
						},
						Action: func(c *cli.Context) error {
							profile := auth.OpenUserDB().CurrentUser()
							if err = profile.SignIn(utils.ApiHost); err != nil {
								return err
							}

							authClientWrapper, err := actions.GetAuthClientWrapper(profile, utils.ApiHost)

							if err != nil {
								return err
							}

							return authClientWrapper.ListLocations(country)
						},
					},
				},
			},
		},
	}

	err = app.Run(os.Args)

	if err != nil {
		sentry.CaptureException(err)
		caser := cases.Title(language.AmericanEnglish)
		msg := strings.Split(err.Error(), " ")
		msg[0] = caser.String(msg[0])
		fmt.Println(strings.Join(msg, " "))

	}
}
