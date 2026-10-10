// CI for hls-indexer: build, lint and unit tests on every push to a feature/* branch.
pipeline {
  agent any

  options {
    skipDefaultCheckout()
    disableConcurrentBuilds()
    timeout(time: 30, unit: 'MINUTES')
  }

  stages {
    stage('Checkout') {
      steps {
        checkout scm
        sh 'git log -1 --oneline'
      }
    }

    stage('Build') {
      steps {
        script {
          docker.build('hls-indexer-ci', 'ci')
          goInside { sh 'go build ./...' }
        }
      }
    }

    stage('Lint') {
      steps {
        script {
          goInside { sh 'golangci-lint run ./...' }
        }
      }
    }

    stage('Test') {
      steps {
        script {
          // Tests need Redis: DB 0 holds claims, each package takes its own DB.
          docker.image('redis:8-alpine').withRun('', 'redis-server --save "" --appendonly no') { redis ->
            sh "until docker exec ${redis.id} redis-cli ping; do sleep 1; done"
            goInside("--link ${redis.id}:redis -e TEST_REDIS_URL=redis://redis:6379/0") {
              sh 'gotestsum --format testname --junitfile report/junit.xml -- ./...'
            }
          }
        }
      }
      post {
        always {
          junit allowEmptyResults: true, testResults: 'report/junit.xml'
        }
      }
    }
  }
}

// goInside runs steps in the CI image with Go caches kept in the workspace.
def goInside(String args = '', Closure body) {
  def cache = "${env.WORKSPACE}/.cache"
  docker.image('hls-indexer-ci').inside("${args} -e GOCACHE=${cache}/go-build -e GOMODCACHE=${cache}/go-mod -e GOLANGCI_LINT_CACHE=${cache}/golangci-lint") {
    body()
  }
}
