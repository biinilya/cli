package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/forestvpn/cli/actions"
	"github.com/forestvpn/cli/api"
	"github.com/forestvpn/cli/auth"
	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/fatih/color"
	"github.com/getsentry/sentry-go"
	"github.com/urfave/cli/v2"
)

var (
	// DSN is a Data Source Name for Sentry. It is stored in an environment variable and assigned during the build with ldflags.
	//
	// See https://docs.sentry.io/product/sentry-basics/dsn-explainer/ for more information.
	DSN = os.Getenv("SENTRY_DSN")
	// appVersion value is stored in an environment variable and assigned during the build with ldflags.
	appVersion = os.Getenv("VERSION")
	// firebaseApiKey is stored in an environment variable and assigned during the build with ldflags.
	firebaseApiKey = os.Getenv("STAGING_FIREBASE_API_KEY")
	// ApiHost is a hostname of Forest VPN back-end API that is stored in an environment variable and assigned during the build with ldflags.
	ApiHost = os.Getenv("STAGING_API_URL")
)

func main() {
	// email is user's email address used to sign in or sign up on the Firebase.
	var email string
	// password is user's password used during sign in or sign up on the Firebase.
	var password string
	// country is stores prompted country name to filter locations by country.
	var country string
	// includeRoutes is a flag that indicates wether to route networks from system routing table into Wireguard tunnel interface.
	var includeRoutes bool

	err := auth.Init()

	if err != nil {
		panic(err)
	}

	authClient := auth.AuthClient{ApiKey: firebaseApiKey}

	if auth.IsRefreshTokenExists() {
		refreshToken, _ := auth.LoadRefreshToken()
		response, err := authClient.GetAccessToken(refreshToken)

		if err == nil {
			auth.JsonDump(response.Body(), auth.FirebaseAuthFile)
		}
	}

	accessToken, _ := auth.LoadAccessToken()
	wrapper := api.GetApiClient(accessToken, ApiHost)
	apiClient := actions.AuthClientWrapper{AuthClient: authClient, ApiClient: wrapper}

	err = sentry.Init(sentry.ClientOptions{
		Dsn: DSN,
	})

	if err != nil {
		log.Fatalf("sentry.Init: %s", err)
	}

	defer sentry.Flush(2 * time.Second)

	app := &cli.App{
		EnableBashCompletion: true,
		Suggest:              true,
		Name:                 "fvpn",
		Usage:                "fast, secure, and modern VPN",
		Commands: []*cli.Command{
			{
				Name:  "account",
				Usage: "Manage your account",
				Subcommands: []*cli.Command{
					{
						Name:  "register",
						Usage: "Sign up to use ForestVPN",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:        "email",
								Destination: &email,
								Usage:       "Your email address",
								Value:       "",
								Aliases:     []string{"e"},
							},
							&cli.StringFlag{
								Name:        "password",
								Destination: &password,
								Usage:       "Password must be at least 8 characters long",
								Value:       "",
								Aliases:     []string{"p"},
							},
						},
						Action: func(c *cli.Context) error {
							return apiClient.Register(email, password)
						},
					},
					{
						Name:  "login",
						Usage: "Log into your ForestVPN account",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:        "email",
								Destination: &email,
								Usage:       "Your email address",
								Value:       "",
								Aliases:     []string{"e"},
							},
							&cli.StringFlag{
								Name:        "password",
								Destination: &password,
								Usage:       "Your password",
								Value:       "",
								Aliases:     []string{"p"},
							},
						},
						Action: func(c *cli.Context) error {
							session := auth.LoadSession()

							if !auth.IsRefreshTokenExists() {
								if session["user"] != email {
									err := os.Remove(auth.DeviceFile)

									if err != nil {
										sentry.CaptureException(err)
									}
								}

								deviceID := auth.LoadDeviceID()

								if err != nil {
									sentry.CaptureException(err)
									return err
								}

								err = apiClient.Login(email, password, deviceID)

								if err != nil {
									sentry.CaptureException(err)
									return err
								}

								if session["user"] != email {
									wrapper.AccessToken, err = auth.LoadAccessToken()
									apiClient.ApiClient.AccessToken = wrapper.AccessToken

									if err != nil {
										sentry.CaptureException(err)
										return err
									}

									resp, err := wrapper.GetBillingFeatures()

									if err != nil {
										return err
									}

									billingFeature := resp[0]
									locations, err := wrapper.GetLocations()

									if err != nil {
										return err
									}

									wrappedLocations := actions.GetWrappedLocations(billingFeature, locations)
									var location actions.LocationWrapper
									var randomIndex int
									var locationSet []actions.LocationWrapper

									if actions.IsPremiumUser(billingFeature) {
										locationSet = wrappedLocations
									} else {
										for _, loc := range wrappedLocations {
											if !loc.Premium {
												locationSet = append(locationSet, loc)
											}
										}
									}

									randomIndex = rand.Intn(len(locationSet))
									location = wrappedLocations[randomIndex]
									err = apiClient.SetLocation(billingFeature, location, includeRoutes)

									if err != nil {
										sentry.CaptureException(err)
										return err
									}
								}

								session["user"] = email
								data, err := json.MarshalIndent(session, "", "    ")

								if err != nil {
									sentry.CaptureException(err)
									return err
								}

								err = auth.JsonDump(data, auth.SessionFile)

								if err != nil {
									sentry.CaptureException(err)
									return err
								}

							}

							if auth.IsRefreshTokenExists() {
								color.Green("Logged in")
							}
							return nil
						},
					},
					{
						Name:  "logout",
						Usage: "Log out from your ForestVPN account on this device",
						Action: func(c *cli.Context) error {
							deviceID := auth.LoadDeviceID()
							err = wrapper.DeleteDevice(deviceID)

							if err != nil {
								sentry.CaptureException(err)
								return err
							}

							err := apiClient.Logout()

							if err != nil {
								sentry.CaptureException(err)
								return err
							}

							err = os.Remove(auth.DeviceFile)

							if err != nil {
								sentry.CaptureException(err)
							}

							color.Green("Logged out")
							return err
						},
					},
					// {
					// 	Name:  "account",
					// 	Usage: "Manage multiple accounts",
					// 	Subcommands: []*cli.Command{
					// 		{
					// 			Name:  "show",
					// 			Usage: "Show all user accounts logged in",
					// 		},
					// 		{
					// 			Name:  "default",
					// 			Usage: "Set a default account",
					// 			Flags: []cli.Flag{
					// 				&cli.StringFlag{
					// 					Name:        "email",
					// 					Destination: &email,
					// 					Usage:       "Email address of your account",
					// 					Value:       "",
					// 					Aliases:     []string{"e"},
					// 				},
					// 			},
					// 			Action: func(c *cli.Context) error {
					// 				if !auth.IsAuthenticated() {
					// 					fmt.Println("Are you signed in?")
					// 					color := color.New(color.Faint)
					// 					color.Println("Try 'forest auth signin'")
					// 					return nil
					// 				}
					// 				emailfield, err := auth.GetEmailField(email)

					// 				if err == nil {
					// 					fmt.Println(emailfield.Value)
					// 				} else {
					// 					sentry.CaptureException(err)
					// 				}
					// 				return err
					// 			},
					// 		},
					// 	},
					// },
				},
			},
			{
				Name:  "state",
				Usage: "Control the state of connection",
				Subcommands: []*cli.Command{
					{

						Name:  "up",
						Usage: "Connect to the ForestVPN",
						Flags: []cli.Flag{
							&cli.BoolFlag{
								Name:        "include-routes",
								Destination: &includeRoutes,
								Usage:       "Route all system network interfaces into VPN tunnel",
								Value:       false,
								Aliases:     []string{"i"},
							},
						},
						Action: func(c *cli.Context) error {
							if !auth.IsAuthenticated() {
								fmt.Println("Are you logged in?")
								color := color.New(color.Faint)
								color.Println("Try 'forest account login'")
								return nil
							} else if auth.IsLocationSet() {
								state := actions.State{}
								status := state.GetStatus()

								if status {
									err := state.SetDown(auth.WireguardConfig)

									if err != nil {
										return err
									}
								}

								err := state.SetUp(auth.WireguardConfig)

								if err != nil {
									return err
								}

								status = state.GetStatus()

								if status {
									session := auth.LoadSession()
									color.Green("Connected to %s, %s", session["city"], session["country"])
								} else {
									err = errors.New("state set up error")
									sentry.CaptureException(err)
									return err
								}
							} else {
								fmt.Println("Please, choose the location to connect.")
								color := color.New(color.Faint)
								color.Println("Use 'fvpn location ls' to see available locations.")
							}
							return nil
						},
					},
					{
						Name:        "down",
						Description: "Disconnect from ForestVPN",
						Usage:       "Shut down the connection",
						Action: func(ctx *cli.Context) error {
							if !auth.IsAuthenticated() {
								fmt.Println("Are you logged in?")
								color := color.New(color.Faint)
								color.Println("Try 'forest account login'")
								return nil
							}

							state := actions.State{}
							status := state.GetStatus()

							if status {
								err := state.SetDown(auth.WireguardConfig)

								if err != nil {
									return err
								}

								status := state.GetStatus()

								if !status {
									color.Red("Disconnected")
								} else {
									err = errors.New("state set down error")
									sentry.CaptureException(err)
									return err
								}

							} else {
								color.Red("Not connected")
							}

							return nil
						},
					},
					{
						Name:        "status",
						Description: "See wether connection is active",
						Usage:       "Check the status of the connection",
						Action: func(ctx *cli.Context) error {
							if !auth.IsAuthenticated() {
								fmt.Println("Are you signed in?")
								color := color.New(color.Faint)
								color.Println("Try 'forest auth signin'")
								return nil
							}

							state := actions.State{}
							status := state.GetStatus()

							if status {
								session := auth.LoadSession()

								color.Green("Connected to %s, %s", session["city"], session["country"])
							} else {
								color.Red("Not connected")
							}

							return nil

						},
					},
				},
			},
			{
				Name:  "location",
				Usage: "Manage locations",
				Subcommands: []*cli.Command{
					{
						Name:        "set",
						Description: "Set the default location by specifying `UUID` or `Name`",
						Flags: []cli.Flag{
							&cli.BoolFlag{
								Name:        "include-routes",
								Destination: &includeRoutes,
								Usage:       "Route all system network interfaces into VPN tunnel",
								Value:       false,
								Aliases:     []string{"i"},
							},
						},
						Action: func(cCtx *cli.Context) error {

							if !auth.IsAuthenticated() {
								fmt.Println("Are you logged in?")
								color := color.New(color.Faint)
								color.Println("Try 'forest account login'")
								return nil
							}

							arg := cCtx.Args().Get(0)

							if len(arg) < 1 {
								return errors.New("UUID or name required")
							}

							resp, err := wrapper.GetBillingFeatures()

							if err != nil {
								return err
							}

							billingFeature := resp[0]
							locations, err := wrapper.GetLocations()

							if err != nil {
								return err
							}

							wrappedLocations := actions.GetWrappedLocations(billingFeature, locations)
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
								return fmt.Errorf("no such location: %s", arg)
							}

							country := location.Location.GetCountry()
							city := location.Location.GetName()
							countryName := country.GetName()
							session := map[string]string{
								"city":    city,
								"country": countryName,
							}

							data, err := json.Marshal(session)

							if err != nil {
								sentry.CaptureException(err)
								return err
							}

							auth.JsonDump(data, auth.SessionFile)

							err = apiClient.SetLocation(billingFeature, location, includeRoutes)

							if err != nil {
								sentry.CaptureException(err)
								return err
							}

							color.New(color.FgGreen).Println(fmt.Sprintf("Default location is set to %s, %s", city, countryName))

							return nil
						},
					},
					{
						Name:        "ls",
						Description: "List locations",
						Flags: []cli.Flag{
							&cli.StringFlag{
								Name:        "country",
								Destination: &country,
								Usage:       "Show locations by country",
								Value:       "",
								Aliases:     []string{"c"},
								Required:    false,
							},
						},
						Action: func(c *cli.Context) error {
							return apiClient.ListLocations(country)
						},
					},
				},
			},
			{
				Name:  "version",
				Usage: "Show the version of ForestVPN CLI",
				Action: func(ctx *cli.Context) error {
					fmt.Printf("ForestVPN CLI %s\n", appVersion)
					return nil
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
		color.Red(strings.Join(msg, " "))
	}
}
