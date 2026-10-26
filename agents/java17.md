# Toolchain: Java (17)

- The image pins JDK 17 via `JAVA_VERSION=17` in the Dockerfile; `JAVA_HOME` is set and the JDK bin dir is on `PATH`, so `java` and `javac` resolve to the pinned toolchain. Don't install or switch JDKs inside the container.
- Prefer the repo's build wrapper (`./mvnw ...` or `./gradlew ...`) for builds and tests; don't invoke `javac`/`java` directly or hand-roll build steps.
- Before committing:
  - Run the repo's checks, e.g. `./mvnw verify` (Maven) or `./gradlew check` (Gradle), and make sure the project compiles (`./mvnw compile` / `./gradlew compileJava`) if it has sources.
- Don't change the pinned JDK version unless asked.
