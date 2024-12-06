include .env

stop_container:
	@echo "Stopping crawler-container"
	if [ $$(podman ps -q) ]; then \
		    echo "found and stopped containers"; \
		    podman stop $(podman ps -q); \
	else \
		    echo "no containers running..."; \
	fi

create_container:
	podman run --name ${DB_PODMAN_CONTAINER} -p 5432:5432 -e POSTGRES_USER=${USER} -e POSTGRES_PASSWORD=${PASSWORD} -d postgres:14-alpine

create_db:
	podman exec -it ${DB_PODMAN_CONTAINER} createdb --username=${USER} --owner=${USER} ${DB_NAME}

start_container:
	podman start ${DB_PODMAN_CONTAINER}

create_migrations:
	sqlx migrate add -r init

migrate_up:
		sqlx migrate run --database-url "postgresql://${USER}:${PASSWORD}@${HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable"

migrate_down:
	sqlx migrate revert --database-url "postgresql://${USER}:${PASSWORD}@${HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable"

build:
	if [ -f "${BINARY}" ]; then \
		     rm "${BINARY}"; \
			 echo "Deleted ${BINARY}";	\
	fi
	@echo "Building ${BINARY}"
	go build -o ${BINARY} cmd/server/*.go

run: build
	@echo "Running ${BINARY}"
	./${BINARY}

stop:
	@echo "stopping server..."
	@-pkill -SIGTERM -f "./{BINARY}"
	@echo "server stopped..."
