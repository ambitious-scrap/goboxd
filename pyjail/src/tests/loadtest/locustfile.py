import json
import logging
import os
import random
import sys
from collections import defaultdict
from pathlib import Path

from locust import HttpUser, between, events, task


# Add parent directories to path for imports
sys.path.insert(0, str(Path(__file__).parent.parent.parent))  # For proto
sys.path.insert(0, str(Path(__file__).parent.parent))  # For tests directory

from tests.utils import (
    create_json_payload,
    get_test_name,
    get_testcase_dirs,
    load_testcase,
    validate_response_status_only,
)


logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)


class NsjailServerUser(HttpUser):
    wait_time = between(0.5, 1.5)

    def on_start(self):
        language_filter = os.environ.get("LANGUAGE")
        self.testcase_dirs = get_testcase_dirs(language_filter)
        if not self.testcase_dirs:
            raise ValueError("No test cases found")

        self.test_cases = []
        for testcase_dir in self.testcase_dirs:
            request_text, reply_text = load_testcase(testcase_dir)
            if request_text is not None:
                self.test_cases.append(
                    {
                        "name": get_test_name(testcase_dir),
                        "request": request_text,
                        "expected_reply": reply_text,
                        "payload": create_json_payload(request_text),
                    },
                )

        if not self.test_cases:
            raise ValueError("No valid test cases loaded")

        logger.info(
            f"Loaded {len(self.test_cases)} test cases from {language_filter or 'all languages'}",
        )

    @task
    def execute_code(self):
        test_case = random.choice(self.test_cases)

        with self.client.post(
            "/",
            json=test_case["payload"],
            name=test_case["name"],
            catch_response=True,
        ) as response:
            if response.status_code != 200:
                response.failure(f"HTTP {response.status_code}")
                logger.warning(
                    f"{test_case['name']}: HTTP {response.status_code}",
                )
                return

            try:
                result = response.json()
                if "message" not in result:
                    response.failure("Missing 'message' field")
                    return

                success, reason = validate_response_status_only(
                    result["message"],
                    test_case["expected_reply"],
                )
                if success:
                    response.success()
                else:
                    response.failure(reason)
                    logger.debug(f"{test_case['name']}: {reason}")

            except json.JSONDecodeError:
                response.failure("Invalid JSON")
            except Exception as e:
                response.failure(str(e))


validation_failures = defaultdict(
    lambda: {"count": 0, "reasons": defaultdict(int)},
)
validation_successes = defaultdict(int)


@events.request.add_listener
def on_request(
    request_type,
    name,
    response_time,
    response_length,
    response,
    context,
    exception,
    **kwargs,
):
    global validation_failures, validation_successes

    if exception:
        validation_failures[name]["count"] += 1
        validation_failures[name]["reasons"][str(exception)] += 1
    else:
        validation_successes[name] += 1


@events.test_start.add_listener
def on_test_start(environment, **kwargs):
    global validation_failures, validation_successes
    validation_failures = defaultdict(
        lambda: {"count": 0, "reasons": defaultdict(int)},
    )
    validation_successes = defaultdict(int)

    logger.info("\nStarting nsjail server load test")
    logger.info(f"Target: {environment.host}")
    logger.info(
        f"Users: {environment.parsed_options.num_users if environment.parsed_options else 'N/A'}",
    )


@events.test_stop.add_listener
def on_test_stop(environment, **kwargs):
    print("\n" + "=" * 60)
    print("Validation Summary:")

    total_tests = set(validation_successes.keys()) | set(
        validation_failures.keys(),
    )

    for test_name in sorted(total_tests):
        success_count = validation_successes[test_name]
        failure_data = validation_failures[test_name]
        failure_count = failure_data["count"]
        total = success_count + failure_count

        success_rate = (success_count / total * 100) if total else 0
        print(f"\n{test_name}:")
        print(
            f"  Requests: {total}, Success: {success_count} ({success_rate:.1f}%), "
            f"Failed: {failure_count} ({100 - success_rate:.1f}%)",
        )

        if failure_data["reasons"]:
            for reason, count in failure_data["reasons"].items():
                print(f"    - {reason}: {count}")

    print("=" * 60)


class ConstantLoadUser(NsjailServerUser):
    wait_time = between(0.009, 0.011)


class BurstLoadUser(NsjailServerUser):
    wait_time = between(0.001, 0.005)


class SustainedLoadUser(NsjailServerUser):
    wait_time = between(0.05, 0.1)
