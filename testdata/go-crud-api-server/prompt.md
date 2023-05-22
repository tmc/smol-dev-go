# Go API Server

This document provides the specification for a simple RESTful API server written in Go that supports operations on a user entity.

## Overview

The Simple API Server shall provide the following services:

1. Create a new user.
2. Retrieve user details.
3. Update user details.
4. Delete a user.

All API endpoints shall be based on the following base URL: `http://localhost:3000/api/v1`.

## User Entity

The User entity shall have the following structure:

{
"id": integer,
"name": string,
"email": string,
"dateOfBirth": date,
"createdAt": date,
"updatedAt": date
}


## API Endpoints

### Create a User

**Endpoint:** `POST /users`

**Request Body:**

{
"name": string,
"email": string,
"dateOfBirth": date
}


**Response Body:**

{
"id": integer,
"name": string,
"email": string,
"dateOfBirth": date,
"createdAt": date,
"updatedAt": date
}


### Retrieve a User

**Endpoint:** `GET /users/{id}`

**Response Body:**

{
"id": integer,
"name": string,
"email": string,
"dateOfBirth": date,
"createdAt": date,
"updatedAt": date
}


### Update a User

**Endpoint:** `PUT /users/{id}`

**Request Body:**


{
"name": string,
"email": string,
"dateOfBirth": date
}


**Response Body:**


{
"id": integer,
"name": string,
"email": string,
"dateOfBirth": date,
"createdAt": date,
"updatedAt": date
}


### Delete a User

**Endpoint:** `DELETE /users/{id}`

**Response Body:**

{
"message": string
}


## Error Handling

All API endpoints shall return a HTTP 400 status code for client errors, and a HTTP 500 status code for server errors.

**Error Response Body:**

{
"error": string
}


## Versioning

The version of the API shall be included in the URL to allow for future changes to the API without breaking existing clients.

