plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("com.google.devtools.ksp")
}

android {
    namespace = "com.mase.messenger"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.mase.messenger"
        minSdk = 24
        targetSdk = 34
        versionCode = 3
        versionName = "0.3.0"

    }

    // Two environments: prod talks to the public server over TLS, dev talks to a local
    // server started with `run.sh dev` (port 8081) and may use cleartext.
    // A physical phone needs the developer machine's LAN address:
    //   ./gradlew :app:assembleDevDebug -PmaseDevHost=192.168.1.10
    flavorDimensions += "env"
    productFlavors {
        create("prod") {
            dimension = "env"
            buildConfigField("String", "WS_URL", "\"wss://mase.nemilk.ru/ws\"")
            buildConfigField("String", "MEDIA_URL", "\"https://mase.nemilk.ru\"")
            buildConfigField("String", "INVITE_SCHEME", "\"mase\"")
            manifestPlaceholders["inviteScheme"] = "mase"
        }
        create("dev") {
            dimension = "env"
            applicationIdSuffix = ".dev"
            versionNameSuffix = "-dev"
            val devHost = (project.findProperty("maseDevHost") as String?) ?: "10.0.2.2"
            val devPort = (project.findProperty("maseDevPort") as String?) ?: "8081"
            buildConfigField("String", "WS_URL", "\"ws://$devHost:$devPort/ws\"")
            buildConfigField("String", "MEDIA_URL", "\"http://$devHost:$devPort\"")
            buildConfigField("String", "INVITE_SCHEME", "\"mase-dev\"")
            manifestPlaceholders["inviteScheme"] = "mase-dev"
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro"
            )
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    composeOptions {
        kotlinCompilerExtensionVersion = "1.5.14"
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.activity:activity-compose:1.9.2")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.4")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.8.4")
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.8.4")
    implementation("androidx.navigation:navigation-compose:2.7.7")
    implementation("androidx.datastore:datastore-preferences:1.0.0")
    implementation("androidx.compose.ui:ui:1.6.8")
    implementation("androidx.compose.ui:ui-tooling-preview:1.6.8")
    implementation("androidx.compose.foundation:foundation:1.6.8")
    implementation("androidx.compose.material3:material3:1.2.1")
    implementation("androidx.compose.material:material-icons-extended:1.6.8")
    implementation("androidx.room:room-runtime:2.6.1")
    implementation("androidx.room:room-ktx:2.6.1")
    ksp("androidx.room:room-compiler:2.6.1")

    // Image loading (photo messages)
    implementation("io.coil-kt:coil-compose:2.6.0")

    // WebSocket (replaces raw TCP)
    implementation("com.squareup.okhttp3:okhttp:4.12.0")

    testImplementation("junit:junit:4.13.2")

    debugImplementation("androidx.compose.ui:ui-tooling:1.6.8")
    debugImplementation("androidx.compose.ui:ui-test-manifest:1.6.8")
}
