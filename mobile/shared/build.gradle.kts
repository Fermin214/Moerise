import org.jetbrains.kotlin.gradle.dsl.JvmTarget

plugins {
    alias(libs.plugins.kotlin.multiplatform)
    alias(libs.plugins.android.kmp.library)
    alias(libs.plugins.kotlin.serialization)
    alias(libs.plugins.compose.multiplatform)
    alias(libs.plugins.compose.compiler)
    alias(libs.plugins.ksp)
    alias(libs.plugins.room)
}

val syncRingDirectory = layout.buildDirectory.dir("generated/moerise-icons")
val generateSyncRing by tasks.registering {
    val source = rootProject.layout.projectDirectory.file("../web/src/sync-ring.js")
    inputs.file(source)
    outputs.dir(syncRingDirectory)
    doLast {
        val path = source.asFile.readText().substringAfter("'").substringBefore("'")
        require(path.matches(Regex("[MmAaLlHhVvZz0-9., -]+")))
        val file = syncRingDirectory.get().file("SyncRingPath.kt").asFile
        file.parentFile.mkdirs()
        file.writeText("package io.github.fermin214.moerise.presentation\n\ninternal const val SyncRingPath = \"$path\"\n")
    }
}

kotlin {
    compilerOptions.freeCompilerArgs.add("-Xexpect-actual-classes")
    androidLibrary {
        namespace = "io.github.fermin214.moerise.shared"
        compileSdk = 36
        minSdk = providers.gradleProperty("moerise.minSdk").get().toInt()
        compilerOptions.jvmTarget.set(JvmTarget.JVM_17)
    }
    jvm {
        compilerOptions.jvmTarget.set(JvmTarget.JVM_17)
    }
    sourceSets {
        commonMain.get().kotlin.srcDir(generateSyncRing)
        listOf(androidMain, jvmMain).forEach { it.get().kotlin.srcDir("src/jvmSharedMain/kotlin") }
        commonMain.dependencies {
            implementation(compose.runtime)
            implementation(compose.foundation)
            implementation(compose.material3)
            implementation(libs.room.runtime)
            implementation(libs.sqlite.bundled)
            implementation(libs.coroutines.core)
            implementation(libs.serialization.json)
            implementation(libs.markdown)
            implementation(libs.ktor.client.core)
        }
        commonTest.dependencies {
            implementation(kotlin("test"))
            implementation(libs.ktor.client.mock)
        }
        androidMain.dependencies {
            implementation(libs.ktor.client.okhttp)
            implementation(libs.androidx.activity.compose)
            implementation(libs.androidx.core)
        }
        jvmMain.dependencies { implementation(libs.ktor.client.okhttp) }
    }
}
dependencies {
    add("kspAndroid", libs.room.compiler)
    add("kspJvm", libs.room.compiler)
}
room {
    schemaDirectory("$projectDir/schemas")
}
tasks.withType<Test>().configureEach {
    maxHeapSize = "256m"
    jvmArgs("-XX:ActiveProcessorCount=1")
    testLogging.events("passed", "failed", "skipped")
}
val sharedJvmTest = tasks.named<Test>("jvmTest") {
    exclude("**/CoreIntegrationTest*", "**/StarMapIntegrationTest*")
}
tasks.register<Test>("coreIntegrationTest") {
    dependsOn("jvmTestClasses")
    testClassesDirs = sharedJvmTest.get().testClassesDirs
    classpath = sharedJvmTest.get().classpath
    include("**/CoreIntegrationTest*")
    outputs.upToDateWhen { false }
}
tasks.register<Test>("starsIntegrationTest") {
    dependsOn("jvmTestClasses")
    testClassesDirs = sharedJvmTest.get().testClassesDirs
    classpath = sharedJvmTest.get().classpath
    include("**/StarMapIntegrationTest*")
    outputs.upToDateWhen { false }
}
