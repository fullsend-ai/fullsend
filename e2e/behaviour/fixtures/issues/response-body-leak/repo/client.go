package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type APIClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

type User struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// FetchUser retrieves a user by ID from the API.
func (c *APIClient) FetchUser(id int) (*User, error) {
	url := fmt.Sprintf("%s/users/%d", c.BaseURL, id)
	resp, err := c.HTTPClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("requesting user %d: %w", id, err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("user %d not found", id)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d for user %d", resp.StatusCode, id)
	}

	defer resp.Body.Close()

	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decoding user %d: %w", id, err)
	}
	return &user, nil
}

// FetchUsers retrieves multiple users by ID, skipping any that are not found.
func (c *APIClient) FetchUsers(ids []int) ([]User, error) {
	var users []User
	for _, id := range ids {
		user, err := c.FetchUser(id)
		if err != nil {
			continue
		}
		users = append(users, *user)
	}
	return users, nil
}

func main() {
	client := &APIClient{
		BaseURL:    "https://api.example.com",
		HTTPClient: http.DefaultClient,
	}

	users, err := client.FetchUsers([]int{1, 2, 3, 4, 5})
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	for _, u := range users {
		fmt.Printf("%s <%s>\n", u.Name, u.Email)
	}
}
