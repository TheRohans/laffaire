.PHONY: build clean test

HASH=$(shell git log --pretty=format:'%h' -n 1)

include .env
export

DOCKER_CONTAINER=$(REPOSITORY)/$(PROJECT)

# List all targets in thie file
list:
	@echo $(HASH)
	@echo ""
	@echo "*^-. Go App Template .-^*"
	@echo ""
	@grep -B 1 '^[^#[:space:]].*:' Makefile
	@echo ""

# Install any needed libraries
install:
	go mod tidy

# Run all go unit tests
test:
	go test -v ./...

# Runs the localhost server
start:
	go run cmd/server/main.go

watch:
#	If you want "hot reloading"
#	sudo apt install entr
	find ./ | entr -sr 'go run cmd/server/main.go'

clean:
	rm -rf build

build: clean
	mkdir -p build
#	CC=/opt/homebrew/bin/x86_64-linux-musl-gcc
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
	go build -o build/server \
		-ldflags '-X main.build=$(HASH)' \
		cmd/server/main.go
	cp -R static build/
	cp -R templates build/
	cp -R migrations build/
	cp -R datastore build/

docker_build:
	docker buildx build --platform linux/amd64 -t $(DOCKER_CONTAINER):$(HASH) .

docker_push:
	docker push $(DOCKER_CONTAINER):$(HASH)

#	Using a different enviroment variable set for prod
docker_run:
	docker ps ; \
	docker run --env-file=.env.production -p 8080:3000 $(DOCKER_CONTAINER):$(HASH)

google:
	gcloud auth application-default set-quota-project $(PROJECT_ID)
	gcloud config set project $(PROJECT_ID)
	gcloud services enable run.googleapis.com
	gcloud projects add-iam-policy-binding $(PROJECT_ID) \
		--member=serviceAccount:$(PROJECT_NUMBER)-compute@developer.gserviceaccount.com \
		--role=roles/cloudbuild.builds.builder

start_db:
	docker ps ; \
	docker run --name postgres \
		-e POSTGRES_USER=$(DB_USER) \
		-e POSTGRES_PASSWORD=$(DB_PASSWORD) \
		-e POSTGRES_DB=$(DB_NAME) \
		-e PGDATA=$(DB_PATH) \
		-p 5432:5432 \
		-d postgres:latest
