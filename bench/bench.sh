#!/bin/bash
# Runs the same notes API on popular stacks and measures each with
# bench/measure. Linux x86_64 only. Everything it downloads, builds or writes
# lives under BENCH_DIR, so `clean` leaves the machine as it was.
#
#   BENCH_DIR=/path/to/scratch bench/bench.sh setup
#   BENCH_DIR=/path/to/scratch bench/bench.sh run <results-dir> [stack...]
#   BENCH_DIR=/path/to/scratch bench/bench.sh clean
#
# setup expects BENCH_DIR/src to hold this bench/ directory as src/bench, the
# Sideyard starter built for linux/amd64 as src/sideyard-app, and measure
# built for linux/amd64 as src/measure. The rails stack also needs Ruby 3.2+
# with its headers, libpq-dev and libyaml-dev from the system's packages; it's
# skipped when ruby isn't installed.
set -euo pipefail

W=${BENCH_DIR:?set BENCH_DIR}
SRC=$W/src
RT=$W/rt
APPS=$W/apps
DATA=$W/data
RUN=$W/run

APP_PORT=47101
MONGO_PORT=47201
PG_PORT=47202
PG_URL=postgresql://bench@127.0.0.1:$PG_PORT/notes

NODE_VERSION=v24.22.1
MONGO_TGZ=https://fastdl.mongodb.org/linux/mongodb-linux-x86_64-ubuntu2404-9.0.4.tgz
MONGO_SHA256=5e85bf685450287a1e7e073da3ae39677ffe7f00c01a86dec31283ccc2cf2dcc

JAVA_JDK=https://github.com/adoptium/temurin25-binaries/releases/download/jdk-25.0.4.1%2B1/OpenJDK25U-jdk_x64_linux_hotspot_25.0.4.1_1.tar.gz
JAVA_JDK_SHA256=dbb698396d478e7fa2b1e50f4103324b2a99b90569ee27c33f2261f9215cf41e
JAVA_JRE=https://github.com/adoptium/temurin25-binaries/releases/download/jdk-25.0.4.1%2B1/OpenJDK25U-jre_x64_linux_hotspot_25.0.4.1_1.tar.gz
JAVA_JRE_SHA256=1731a34baadec5479258ea0202e4d5d865d2efeee60cb0c7d7eb056fe96ca219

ALL_STACKS="sideyard next mern flask django fastapi spring rails"

export GEM_HOME=$RT/gems
export PATH=$GEM_HOME/bin:$RT/node/bin:$RT/pg/usr/lib/postgresql/16/bin:$RT/mongodb/bin:$PATH
export NEXT_TELEMETRY_DISABLED=1 npm_config_update_notifier=false npm_config_fund=false npm_config_audit=false
export PIP_DISABLE_PIP_VERSION_CHECK=1
# Package managers cache in the home directory by default; keep it all here.
export XDG_CACHE_HOME=$W/cache npm_config_cache=$W/cache/npm PIP_CACHE_DIR=$W/cache/pip
export GEM_SPEC_CACHE=$W/cache/gem-specs BUNDLE_USER_HOME=$W/cache/bundle

setup() {
	mkdir -p "$RT" "$APPS" "$DATA" "$RUN"

	if [ ! -x "$RT/node/bin/node" ]; then
		local f=node-$NODE_VERSION-linux-x64.tar.xz
		curl -fsSLo "$RT/$f" "https://nodejs.org/dist/$NODE_VERSION/$f"
		curl -fsSL "https://nodejs.org/dist/$NODE_VERSION/SHASUMS256.txt" | grep " $f\$" | (cd "$RT" && sha256sum -c -)
		mkdir "$RT/node" && tar -xJf "$RT/$f" -C "$RT/node" --strip-components=1 && rm "$RT/$f"
	fi
	if [ ! -x "$RT/mongodb/bin/mongod" ]; then
		curl -fsSLo "$RT/mongodb.tgz" "$MONGO_TGZ"
		echo "$MONGO_SHA256  $RT/mongodb.tgz" | sha256sum -c -
		mkdir "$RT/mongodb" && tar -xzf "$RT/mongodb.tgz" -C "$RT/mongodb" --strip-components=1 && rm "$RT/mongodb.tgz"
	fi
	# Ubuntu's own Postgres packages, unpacked rather than installed.
	if [ ! -x "$RT/pg/usr/lib/postgresql/16/bin/postgres" ]; then
		mkdir -p "$RT/debs"
		(cd "$RT/debs" && apt-get download postgresql-16 postgresql-client-16)
		for d in "$RT"/debs/*.deb; do dpkg -x "$d" "$RT/pg"; done
		rm -rf "$RT/debs"
	fi
	if ldd "$RT/pg/usr/lib/postgresql/16/bin/postgres" | grep 'not found'; then
		echo "postgres is missing the libraries above" >&2
		exit 1
	fi

	mkdir -p "$APPS/sideyard"
	cp "$SRC/sideyard-app" "$APPS/sideyard/app"

	fresh mern
	(cd "$APPS/mern" && npm install --silent express mongoose)

	fresh next
	(cd "$APPS/next" &&
		npm install --silent next react react-dom @prisma/client@7 @prisma/adapter-pg@7 pg &&
		npm install --silent -D prisma@7 typescript @types/node @types/react @types/pg &&
		DATABASE_URL=$PG_URL npm run build)

	for py in flask django fastapi; do
		fresh $py
		python3 -m venv "$APPS/$py/.venv"
		"$APPS/$py/.venv/bin/pip" install -q -r "$APPS/$py/requirements.txt"
	done
	(cd "$APPS/django" && DATABASE_URL=$PG_URL .venv/bin/python manage.py makemigrations notes -v 0)

	# Spring Boot: a fresh project from start.spring.io with the notes classes added,
	# built with the JDK and run on the smaller JRE.
	[ -x "$RT/jdk/bin/java" ] || fetch "$JAVA_JDK" "$JAVA_JDK_SHA256" "$RT/jdk"
	[ -x "$RT/jre/bin/java" ] || fetch "$JAVA_JRE" "$JAVA_JRE_SHA256" "$RT/jre"
	rm -rf "$APPS/spring" && mkdir -p "$APPS/spring"
	curl -fsSL https://start.spring.io/starter.tgz -d type=maven-project -d language=java -d javaVersion=25 \
		-d dependencies=web,data-jpa,postgresql -d groupId=bench -d artifactId=notes -d name=notes \
		-d packageName=bench.notes | tar -xzf - -C "$APPS/spring"
	cp -r "$SRC/bench/stacks/spring/." "$APPS/spring/"
	(cd "$APPS/spring" && JAVA_HOME=$RT/jdk MAVEN_USER_HOME=$RT/m2 \
		./mvnw -q -Dmaven.repo.local="$RT/m2/repository" package -DskipTests &&
		cp target/notes-0.0.1-SNAPSHOT.jar app.jar)

	# Rails: a fresh API-only app with the notes model, controllers and routes added.
	if command -v ruby >/dev/null; then
		gem install --no-document rails bundler
		rm -rf "$APPS/rails" && mkdir -p "$APPS/rails"
		(cd "$APPS/rails" && rails new notes --api -d postgresql --skip-git --skip-docker --skip-kamal \
			--skip-thruster --skip-ci --skip-test --skip-brakeman --skip-rubocop --skip-bundle)
		cp -r "$SRC/bench/stacks/rails/." "$APPS/rails/notes/"
		(cd "$APPS/rails/notes" &&
			bundle config set --local path vendor/bundle &&
			bundle install --quiet &&
			bin/rails generate model Note body:text --quiet &&
			sed -i 's/^\( *config\.\(force\|assume\)_ssl\) = true/\1 = false/' config/environments/production.rb)
	fi
}

fetch() {
	curl -fsSLo "$RT/download.tgz" "$1"
	echo "$2  $RT/download.tgz" | sha256sum -c -
	mkdir -p "$3" && tar -xzf "$RT/download.tgz" -C "$3" --strip-components=1 && rm "$RT/download.tgz"
}

fresh() {
	rm -rf "${APPS:?}/$1"
	cp -r "$SRC/bench/stacks/$1" "$APPS/$1"
}

# start runs a command in its own process group, so stop can end all of it.
start() {
	setsid "$@" >>"$RUN/$STACK.log" 2>&1 &
	echo $!
}

# stop signals a process group and waits; an overloaded server can ignore the
# signal (next-server does), so after 15 s it's killed.
stop() {
	kill -"${2:-TERM}" -- "-$1" 2>/dev/null || true
	for _ in $(seq 75); do
		kill -0 "$1" 2>/dev/null || return 0
		sleep 0.2
	done
	echo "$STACK: $1 didn't stop within 15 s; killing it" >&2
	kill -KILL -- "-$1" 2>/dev/null || true
	while kill -0 "$1" 2>/dev/null; do sleep 0.2; done
}

# wait_for retries a command every 0.2 s for up to a minute.
wait_for() {
	for _ in $(seq 300); do
		"$@" 2>/dev/null && return
		sleep 0.2
	done
	echo "gave up waiting for: $*" >&2
	return 1
}

tcp_open() { (exec 3<>"/dev/tcp/127.0.0.1/$1"); }

pg_start() {
	rm -rf "$DATA/pg"
	initdb -D "$DATA/pg" -U bench --auth=trust --locale=C.UTF-8 >/dev/null
	PG_PID=$(start postgres -D "$DATA/pg" -p $PG_PORT -k "$RUN" -c listen_addresses=127.0.0.1)
	wait_for pg_isready -q -h 127.0.0.1 -p $PG_PORT
	createdb -h 127.0.0.1 -p $PG_PORT -U bench notes
}

mongo_start() {
	rm -rf "$DATA/mongo" && mkdir -p "$DATA/mongo"
	# The cache mongod would pick on a 4 GB server: half of (RAM - 1 GB).
	MONGO_PID=$(start mongod --dbpath "$DATA/mongo" --port $MONGO_PORT --bind_ip 127.0.0.1 \
		--wiredTigerCacheSizeGB 1.5 --logpath "$DATA/mongod.log")
	wait_for tcp_open $MONGO_PORT
}

# healthy waits for the app's health check and prints the ms since $1 (ns).
healthy() {
	for _ in $(seq 600); do
		if curl -fs -o /dev/null "http://127.0.0.1:$APP_PORT/healthz"; then
			echo $((($(date +%s%N) - $1) / 1000000))
			return
		fi
		sleep 0.1
	done
	echo "$STACK isn't healthy after 60 s; see $RUN/$STACK.log" >&2
	return 1
}

mb() { du -scb "$@" | tail -1 | awk '{printf "%.1f", $1 / 1048576}'; }

PY_RUNTIME="/usr/lib/python3.12 /usr/bin/python3.12"
RUBY_RUNTIME="/usr/lib/ruby /usr/bin/ruby3.2 /usr/lib/x86_64-linux-gnu/libruby-3.2.so.3.2"
# Multi-process servers get 4 workers, one per core on the test machine, like gunicorn and uvicorn here.
RAILS_ENV_VARS="RAILS_ENV=production SECRET_KEY_BASE=benchmark-only-0123456789abcdef RAILS_LOG_LEVEL=warn WEB_CONCURRENCY=4 PGHOST=127.0.0.1 PGPORT=$PG_PORT"
PG_SERVER="$RT/pg/usr/lib/postgresql $RT/pg/usr/share/postgresql"

run_stack() {
	STACK=$1
	local out=$2 app procs disk t0 ms name db=""
	cd "$APPS/$STACK"
	case $STACK in
	sideyard)
		name="Go + SQLite (Sideyard)"
		rm -rf "$DATA/sideyard"
		t0=$(date +%s%N)
		app=$(start env PORT=$APP_PORT DATA_DIR="$DATA/sideyard" ./app)
		disk=$(mb app)
		;;
	mern)
		name="MERN: Express + MongoDB"
		mongo_start
		db=$MONGO_PID
		t0=$(date +%s%N)
		app=$(start env PORT=$APP_PORT MONGO_URL=mongodb://127.0.0.1:$MONGO_PORT/notes NODE_ENV=production node server.js)
		disk=$(mb "$RT/node" . "$RT/mongodb/bin/mongod")
		;;
	next)
		name="Next.js + Prisma + Postgres"
		pg_start
		db=$PG_PID
		DATABASE_URL=$PG_URL npx prisma db push >/dev/null
		t0=$(date +%s%N)
		app=$(start env DATABASE_URL=$PG_URL NODE_ENV=production node_modules/.bin/next start -H 127.0.0.1 -p $APP_PORT)
		disk=$(mb "$RT/node" . $PG_SERVER)
		;;
	flask)
		name="Flask + Postgres (gunicorn)"
		pg_start
		db=$PG_PID
		DATABASE_URL=${PG_URL/postgresql:/postgresql+psycopg:} .venv/bin/python -c 'import app'
		t0=$(date +%s%N)
		app=$(start env DATABASE_URL="${PG_URL/postgresql:/postgresql+psycopg:}" .venv/bin/gunicorn -w 4 -b 127.0.0.1:$APP_PORT app:app)
		disk=$(mb .venv . $PY_RUNTIME $PG_SERVER)
		;;
	django)
		name="Django + Postgres (gunicorn)"
		pg_start
		db=$PG_PID
		DATABASE_URL=$PG_URL .venv/bin/python manage.py migrate -v 0
		t0=$(date +%s%N)
		app=$(start env DATABASE_URL=$PG_URL .venv/bin/gunicorn -w 4 -b 127.0.0.1:$APP_PORT notesproj.wsgi)
		disk=$(mb .venv . $PY_RUNTIME $PG_SERVER)
		;;
	fastapi)
		name="FastAPI + Postgres (uvicorn)"
		pg_start
		db=$PG_PID
		DATABASE_URL=${PG_URL/postgresql:/postgresql+psycopg:} .venv/bin/python -c 'import main'
		t0=$(date +%s%N)
		app=$(start env DATABASE_URL="${PG_URL/postgresql:/postgresql+psycopg:}" .venv/bin/uvicorn main:app --host 127.0.0.1 --port $APP_PORT --workers 4 --no-access-log)
		disk=$(mb .venv . $PY_RUNTIME $PG_SERVER)
		;;
	spring)
		name="Spring Boot + Postgres"
		pg_start
		db=$PG_PID
		t0=$(date +%s%N)
		# MaxRAM sizes the JVM as it would size itself on a 4 GB server.
		app=$(start "$RT/jre/bin/java" -XX:MaxRAM=4g -jar app.jar --server.address=127.0.0.1 --server.port=$APP_PORT \
			--spring.datasource.url=jdbc:postgresql://127.0.0.1:$PG_PORT/notes --spring.datasource.username=bench \
			--spring.jpa.hibernate.ddl-auto=update)
		disk=$(mb "$RT/jre" app.jar $PG_SERVER)
		;;
	rails)
		name="Rails + Postgres (Puma, 4 workers)"
		pg_start
		db=$PG_PID
		createuser -h 127.0.0.1 -p $PG_PORT -U bench -s notes
		cd notes
		env $RAILS_ENV_VARS bin/rails db:prepare >/dev/null
		t0=$(date +%s%N)
		app=$(start env $RAILS_ENV_VARS bin/rails server -b 127.0.0.1 -p $APP_PORT)
		disk=$(mb . $RUBY_RUNTIME $PG_SERVER)
		;;
	*)
		echo "unknown stack $STACK" >&2
		return 1
		;;
	esac
	ms=$(healthy "$t0")
	procs="app=$app"
	[ -z "$db" ] || procs="$procs,db=$db"
	"$SRC/measure" -name "$name" -url "http://127.0.0.1:$APP_PORT" -procs "$procs" \
		-startup-ms "$ms" -disk-mb "$disk" -out "$out/$STACK.json" ${MEASURE_FLAGS:-}
	stop "$app"
	[ -z "$db" ] || stop "$db" INT
}

run() {
	local out=${1:?results directory}
	shift
	mkdir -p "$out" "$RUN"
	{
		uname -srm
		nproc
		node --version
		mongod --version | head -1
		postgres --version
		python3 --version
		(cd "$APPS/next" && npm ls --depth=0 2>/dev/null | tail -n +2)
		(cd "$APPS/mern" && npm ls --depth=0 2>/dev/null | tail -n +2)
		for py in flask django fastapi; do "$APPS/$py/.venv/bin/pip" freeze | sed "s/^/$py: /"; done
		"$RT/jre/bin/java" -version 2>&1 | head -1
		grep -A1 '<artifactId>spring-boot-starter-parent' "$APPS/spring/pom.xml" | tail -1
		if [ -d "$APPS/rails/notes" ]; then ruby --version && (cd "$APPS/rails/notes" && bundle exec rails --version); fi
	} >"$out/versions.txt"
	for s in ${*:-$ALL_STACKS}; do
		run_stack "$s" "$out"
	done
}

clean() {
	pkill -f "$W/" 2>/dev/null || true
	rm -rf "${W:?}"
}

"$@"
